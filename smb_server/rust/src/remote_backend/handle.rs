//! ============================================================================
//! remote_backend/handle.rs —— RemoteHandle:一次打开的远程句柄存根
//! ============================================================================
//!
//! 对应一次 SMB CREATE 在 Rust 侧的本地存根:句柄生命周期内的全部文件操作
//! (read/write/flush/stat/set_times/truncate/list_dir/close)均翻译为对
//! Go 网关的帧协议 RPC(经 backend.rs 的 call_operate 统一入口)。
//!
//! 线程安全:仅持 Arc<GatewayClient> 与句柄 ID,满足 Handle 的
//! Send + Sync + 'static 约束。

use std::sync::Arc; // Arc:共享网关连接

use async_trait::async_trait; // async trait 支持(库的 trait 为 async)
use bytes::Bytes; // 字节缓冲(读数据返回)
use smb_server::{DirEntry, FileInfo, FileTimes, Handle, SmbError, SmbResult}; // 库的 SMB 抽象类型

use super::backend::{call_operate, map_gateway_err}; // 控制面调用 + 错误映射
use super::client::GatewayClient; // 帧协议客户端
use crate::types::*; // 帧协议契约(操作码/参数/响应)

/// 远程句柄:对应一次 SMB CREATE 的本地存根。
///
/// 关键字段意义:
/// - `conn`:到 Go 网关的连接(共享);
/// - `handle_id`:Go 侧分配的远程句柄 ID(后续 RPC 的定位依据);
/// - `is_dir`:文件/目录区分(协议层不允许对目录读写数据);
/// - `delete_on_close`:FILE_DELETE_ON_CLOSE,close() 时删除;
/// - `user_id`:打开者的 users.id(句柄生命周期内所有操作帧携带,Go 侧审查)。
#[derive(Clone)] // 可克隆(共享同一远程句柄)
pub(super) struct RemoteHandle {
    conn: Arc<GatewayClient>, // 到 Go 网关的连接
    handle_id: u64, // 远程句柄 ID(Go 侧 HandleRegistry 分配)
    is_dir: bool, // 是否目录句柄
    delete_on_close: bool, // 关闭时删除
    user_id: u64, // 打开者 users.id(操作身份)
}

impl RemoteHandle {
    /// 构造远程句柄存根(backend.open 成功后调用)。
    pub(super) fn new(
        conn: Arc<GatewayClient>,
        handle_id: u64,
        is_dir: bool,
        delete_on_close: bool,
        user_id: u64,
    ) -> Self {
        Self {
            conn,
            handle_id,
            is_dir,
            delete_on_close,
            user_id,
        }
    }
}

/// 实现库定义的 `Handle` trait(一次打开的文件/目录)。
#[async_trait]
impl Handle for RemoteHandle {
    /// 按偏移读取(SMB READ)。
    ///
    /// 参数:`offset` 起始偏移;`len` 期望长度。
    /// 返回值:Ok(Bytes)(实际字节,可少于请求 = EOF);Err(SmbError)。
    ///
    /// 实现步骤:
    /// 1. 目录句柄直接报"是目录"错误;
    /// 2. 组装按偏移读请求发给网关;
    /// 3. 应答体按协议先解实际长度,再取流数据段;
    /// 4. 实际长度为 0 表示读到文件尾;错误码映射为 SMB 错误。
    async fn read(&self, offset: u64, len: u32) -> SmbResult<Bytes> {
        let _t = std::time::Instant::now(); // 计时起点
        if self.is_dir {
            // 目录句柄?
            return Err(SmbError::IsDirectory); // 目录不能读数据
        }
        let req = OperateRequest {
            // 组装读请求
            code: CODE_FILE_READ, // 操作码:读
            user_id: self.user_id, // 操作者身份(Go 侧审查/落库)
            read: Some(ReadArgs {
                // 读参数
                handle_id: self.handle_id, // 远程句柄
                offset, // 起始偏移
                length: len, // 期望长度
            }),
            ..Default::default() // 其余字段 None
        };
        let (resp, stream) = call_operate(&self.conn, &req, &[]) // 发网关(无流段;响应带流段)
            .await
            .map_err(map_gateway_err)?; // 错误码映射
        let r = resp.read.ok_or(SmbError::NotSupported)?; // 取读结果
        if r.length as usize != stream.len() {
            // 网关声明长度 ≠ 流段实际长度?
            errorf!("后端:读取长度不符 句柄={} 期望={} 实得={}", &["SMB"], self.handle_id, r.length, stream.len()); // 协议错位:记录详情
            return Err(SmbError::Io(std::io::Error::other("read length mismatch"))); // 防错位,报 IO 错误
        } // 长度契约校验通过
        debugf!("后端:读取成功 句柄={} 偏移={} 长度={} → {} 字节(耗时 {}µs)", &["SMB"], self.handle_id, offset, len, stream.len(), _t.elapsed().as_micros()); // 记录读结果与耗时
        Ok(Bytes::from(stream)) // 流段即读到的数据
    }

    /// 按偏移写入(SMB WRITE;Go 侧走写回缓存)。
    ///
    /// 参数:`offset` 写入偏移;`data` 待写数据。
    /// 返回值:Ok(u32)(实际写入字节数);Err(SmbError)。
    ///
    /// 实现步骤:
    /// 1. 目录句柄直接报"是目录"错误;
    /// 2. 组装写请求(参数 JSON)+ 数据流段;
    /// 3. 发给网关,取回实际写入字节数。
    async fn write(&self, offset: u64, data: &[u8]) -> SmbResult<u32> {
        let _t = std::time::Instant::now(); // 计时起点
        if self.is_dir {
            // 目录句柄?
            return Err(SmbError::IsDirectory); // 目录不能写数据
        }
        let req = OperateRequest {
            // 组装写请求
            code: CODE_FILE_WRITE, // 操作码:写
            user_id: self.user_id, // 操作者身份(Go 侧审查/落库)
            write: Some(WriteArgs {
                // 写参数
                handle_id: self.handle_id, // 远程句柄
                offset, // 写入偏移
            }),
            ..Default::default() // 其余字段 None
        };
        let (resp, _) = call_operate(&self.conn, &req, data) // 发网关(data 走流数据段)
            .await
            .map_err(map_gateway_err)?; // 错误码映射
        let r = resp.write.ok_or(SmbError::NotSupported)?; // 取写结果
        debugf!("后端:写入成功 句柄={} 偏移={} → {} 字节(耗时 {}µs)", &["SMB"], self.handle_id, offset, r.written, _t.elapsed().as_micros()); // 记录写结果与耗时
        Ok(r.written) // 实际写入字节数
    }

    /// 冲刷缓冲(SMB FLUSH;触发 Go 侧写回缓存整体上传)。
    ///
    /// 参数:无。
    /// 返回值:Ok(());Err(SmbError)。
    ///
    /// 实现步骤:
    /// 1. 组装冲刷请求发给网关(触发写回缓存整体上传);
    /// 2. 错误码映射为 SMB 错误。
    async fn flush(&self) -> SmbResult<()> {
        let _t = std::time::Instant::now(); // 计时起点
        let req = OperateRequest {
            // 组装冲刷请求
            code: CODE_FILE_FLUSH, // 操作码:冲刷
            user_id: self.user_id, // 操作者身份(Go 侧审查/落库)
            flush: Some(FlushArgs {
                // 冲刷参数
                handle_id: self.handle_id, // 远程句柄
            }),
            ..Default::default() // 其余字段 None
        };
        let (_, _) = call_operate(&self.conn, &req, &[]) // 发网关(触发写回缓存上传)
            .await
            .map_err(map_gateway_err)?; // 错误码映射
        debugf!("后端:冲刷成功 句柄={}(耗时 {}µs)", &["SMB"], self.handle_id, _t.elapsed().as_micros()); // 记录冲刷成功与耗时
        infof!("后端:文件变更 写回 user={} 句柄={}", &["SMB"], self.user_id, self.handle_id); // 每次文件变更仅记录一次(Info 级:操作者+被操作文件)
        Ok(())
    }

    /// 查询文件信息(SMB QUERY_INFO)。
    ///
    /// 参数:无。
    /// 返回值:Ok(FileInfo)(字段与 Go 侧/库定义一致);Err(SmbError)。
    ///
    /// 实现步骤:
    /// 1. 组装元信息查询请求发给网关;
    /// 2. 把应答的元信息字段逐一搬到库的元信息结构返回。
    async fn stat(&self) -> SmbResult<FileInfo> {
        let _t = std::time::Instant::now(); // 计时起点
        let req = OperateRequest {
            // 组装查询请求
            code: CODE_FILE_STAT, // 操作码:查元信息
            user_id: self.user_id, // 操作者身份(Go 侧审查/落库)
            stat: Some(StatArgs {
                // 查询参数
                handle_id: self.handle_id, // 远程句柄
            }),
            ..Default::default() // 其余字段 None
        };
        let (resp, _) = call_operate(&self.conn, &req, &[]) // 发网关
            .await
            .map_err(map_gateway_err)?; // 错误码映射
        let r = resp.stat.ok_or(SmbError::NotSupported)?; // 取查询结果
        debugf!("后端:查询信息成功 句柄={} 名称={} 大小={} 目录={}(耗时 {}µs)", &["SMB"], self.handle_id, r.info.name, r.info.end_of_file, r.info.is_directory, _t.elapsed().as_micros()); // 记录元信息结果与耗时
        Ok(FileInfo {
            // 帧协议 FileInfo → 库 FileInfo(字段一一对应直接搬运)
            name: r.info.name, // 显示名
            end_of_file: r.info.end_of_file, // 文件大小
            allocation_size: r.info.allocation_size, // 分配大小
            creation_time: r.info.creation_time, // 创建时间(FILETIME)
            last_access_time: r.info.last_access_time, // 最后访问
            last_write_time: r.info.last_write_time, // 最后写入
            change_time: r.info.change_time, // 变更时间
            is_directory: r.info.is_directory, // 是否目录
            file_index: r.info.file_index, // 文件索引
        })
    }

    /// 设置时间戳(SMB SET_INFO / FILE_BASIC_INFORMATION)。
    ///
    /// 参数:`times` 可选 FILETIME 值(None = 不改)。
    /// 返回值:Ok(());Err(SmbError)。
    ///
    /// 实现步骤:
    /// 1. 把要修改的时间戳(可空)组装进请求,发给网关;
    /// 2. 错误码映射为 SMB 错误。
    async fn set_times(&self, times: FileTimes) -> SmbResult<()> {
        let _t = std::time::Instant::now(); // 计时起点
        let req = OperateRequest {
            // 组装时间戳请求
            code: CODE_FILE_SET_TIMES, // 操作码:设置时间戳
            user_id: self.user_id, // 操作者身份(Go 侧审查/落库)
            set_times: Some(SetTimesArgs {
                // 参数
                handle_id: self.handle_id, // 远程句柄
                creation_time: times.creation_time, // 创建时间(None = 不改)
                last_access_time: times.last_access_time, // 最后访问
                last_write_time: times.last_write_time, // 最后写入
                change_time: times.change_time, // 变更时间
            }),
            ..Default::default() // 其余字段 None
        };
        let (_, _) = call_operate(&self.conn, &req, &[]) // 发网关
            .await
            .map_err(map_gateway_err)?; // 错误码映射
        debugf!("后端:设置时间成功 句柄={}(耗时 {}µs)", &["SMB"], self.handle_id, _t.elapsed().as_micros()); // 记录设置成功与耗时
        Ok(())
    }

    /// 截断/扩展文件到指定长度(SMB SET_END_OF_FILE)。
    ///
    /// 参数:`len` 目标长度。
    /// 返回值:Ok(());Err(SmbError)。
    ///
    /// 实现步骤:
    /// 1. 组装截断请求(目标长度)发给网关;
    /// 2. 错误码映射为 SMB 错误。
    async fn truncate(&self, len: u64) -> SmbResult<()> {
        let _t = std::time::Instant::now(); // 计时起点
        let req = OperateRequest {
            // 组装截断请求
            code: CODE_FILE_TRUNCATE, // 操作码:截断/扩展
            user_id: self.user_id, // 操作者身份(Go 侧审查/落库)
            truncate: Some(TruncateArgs {
                // 参数
                handle_id: self.handle_id, // 远程句柄
                length: len, // 目标长度
            }),
            ..Default::default() // 其余字段 None
        };
        let (_, _) = call_operate(&self.conn, &req, &[]) // 发网关
            .await
            .map_err(map_gateway_err)?; // 错误码映射
        debugf!("后端:截断成功 句柄={} 长度={}(耗时 {}µs)", &["SMB"], self.handle_id, len, _t.elapsed().as_micros()); // 记录截断成功与耗时
        Ok(())
    }

    /// 列出目录内容(SMB QUERY_DIRECTORY)。
    ///
    /// 参数:`pattern` 通配符(后端可不实现,协议层 dispatcher 后过滤)。
    /// 返回值:Ok(Vec<DirEntry>);Err(SmbError)。
    ///
    /// 实现步骤:
    /// 1. 非目录句柄直接报"不是目录"错误;
    /// 2. 组装列目录请求发给网关;
    /// 3. 把应答的条目列表转成库的目录条目结构。
    async fn list_dir(&self, pattern: Option<&str>) -> SmbResult<Vec<DirEntry>> {
        let _t = std::time::Instant::now(); // 计时起点
        if !self.is_dir {
            // 非目录句柄?
            return Err(SmbError::NotADirectory); // 不能列目录
        }
        let req = OperateRequest {
            // 组装列目录请求
            code: CODE_FILE_LIST_DIR, // 操作码:列目录
            user_id: self.user_id, // 操作者身份(Go 侧审查/落库)
            list_dir: Some(ListDirArgs {
                // 参数
                handle_id: self.handle_id, // 目录句柄
                pattern: pattern.unwrap_or("").to_string(), // 通配符(空 = 全部)
            }),
            ..Default::default() // 其余字段 None
        };
        let (resp, _) = call_operate(&self.conn, &req, &[]) // 发网关
            .await
            .map_err(map_gateway_err)?; // 错误码映射
        let r = resp.list_dir.ok_or(SmbError::NotSupported)?; // 取条目列表
        debugf!("后端:列目录成功 句柄={} 模式={:?} → {} 个条目(耗时 {}µs)", &["SMB"], self.handle_id, pattern, r.entries.len(), _t.elapsed().as_micros()); // 记录条目数与耗时
        let entries = r // 逐条转换:帧协议 FileInfo → 库 FileInfo
            .entries
            .into_iter()
            .map(|info| DirEntry {
                // 包成库目录条目
                info: FileInfo {
                    name: info.name, // 显示名
                    end_of_file: info.end_of_file, // 大小
                    allocation_size: info.allocation_size, // 分配大小
                    creation_time: info.creation_time, // 创建时间
                    last_access_time: info.last_access_time, // 最后访问
                    last_write_time: info.last_write_time, // 最后写入
                    change_time: info.change_time, // 变更时间
                    is_directory: info.is_directory, // 是否目录
                    file_index: info.file_index, // 文件索引
                },
            })
            .collect(); // 收集为 Vec
        Ok(entries)
    }

    /// 关闭句柄(SMB CLOSE;Go 侧触发写回缓存整体上传 + delete_on_close)。
    ///
    /// 参数:无(消费 self)。
    /// 返回值:Ok(());Err(SmbError)(上传失败仍注销句柄,错误仅记录)。
    ///
    /// 实现步骤:
    /// 1. 组装关闭请求发给网关(触发写回缓存整体上传与关闭即删);
    /// 2. 失败仅记日志——句柄已注销,数据一致性由网关侧写回重试兜底。
    async fn close(self: Box<Self>) -> SmbResult<()> {
        let _t = std::time::Instant::now(); // 计时起点
        debugf!("后端:关闭句柄 {} 关闭即删={}", &["SMB"], self.handle_id, self.delete_on_close); // 记录关闭请求
        let req = OperateRequest {
            // 组装关闭请求
            code: CODE_FILE_CLOSE, // 操作码:关闭
            user_id: self.user_id, // 操作者身份(Go 侧审查/落库)
            close: Some(CloseArgs {
                // 参数
                handle_id: self.handle_id, // 远程句柄
            }),
            ..Default::default() // 其余字段 None
        };
        let (_, _) = call_operate(&self.conn, &req, &[]) // 发网关(触发写回上传/关闭即删)
            .await
            .map_err(map_gateway_err)?; // 失败仍注销句柄;一致性由网关写回重试兜底
        debugf!("后端:关闭成功 句柄={}(耗时 {}µs)", &["SMB"], self.handle_id, _t.elapsed().as_micros()); // 记录关闭成功与耗时
        Ok(())
    }
}
