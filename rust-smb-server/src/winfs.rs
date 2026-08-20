//! ============================================================================
//! winfs.rs — Windows 文件系统后端(ShareBackend / Handle 实现)
//! ============================================================================
//!
//! 为什么需要这个文件?
//! ----------------------------------------------------------------------------
//! `ixr-smb-server` 库自带 `LocalFsBackend`,但它依赖 Unix 专有的
//! `std::os::unix::fs::FileExt::read_at/write_at`(位置读写),在 Windows 上
//! 无法编译。而本服务器需要运行在 Windows 上,因此按照库公开的
//! `ShareBackend` / `Handle` trait(存储抽象层),自己实现一个 Windows 版
//! 文件系统后端,挂载为一个 SMB 共享。
//!
//! 设计要点:
//! - 所有 SMB 路径(已验证、无 `..`、无非法字符)通过 `host_path()` 拼接成
//!   宿主机磁盘路径,天然防止目录穿越。
//! - 文件读写全部使用 `std::os::windows::fs::FileExt` 的定位读写
//!   (`seek_read` / `seek_write`),不移动文件游标,天然支持 SMB 的
//!   "按偏移量读写" 语义。
//! - 句柄用 `Mutex<File>` 包一层,因为 trait 要求 `&self` 方法即可读写,
//!   Mutex 提供内部可变性。
//!
//! 踩坑记录:
//! - Windows 上用 `File::open` 打开【目录】会返回 AccessDenied,必须通过
//!   `OpenOptionsExt::custom_flags` 附加 `FILE_FLAG_BACKUP_SEMANTICS`(0x02000000)。
//! - Rust 1.75+ 修改文件时间戳的 API:`std::fs::FileTimes` +
//!   `File::set_times()`,Windows 扩展 trait `FileTimesExt` 提供 `set_created`。
//! - FILETIME 是"从 1601-01-01 起 100ns 刻度数",转 `SystemTime` 需减去
//!   Unix 纪元差值 116444736000000000 再乘 100。

use std::fs::{self, OpenOptions as FsOpenOptions};
use std::io::{self, Write};
use std::os::windows::fs::{FileExt, FileTimesExt, MetadataExt, OpenOptionsExt};
use std::path::PathBuf;
use std::sync::Mutex;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use async_trait::async_trait;
use bytes::Bytes;
use smb_server::{
    BackendCapabilities, DirEntry, FileInfo, FileTimes, Handle, OpenIntent, OpenOptions,
    ShareBackend, SmbError, SmbResult, SmbPath,
};

// ============================================================================
// 后端主体:WinFsBackend
// ============================================================================

/// Windows 本地文件系统后端:把磁盘上的一个根目录(sandbox)暴露为一个 SMB 共享。
///
/// 线程安全:struct 本身只持有不可变的根路径,所有可变状态都在句柄
/// (`WinHandle`)内部;`ShareBackend` trait 要求 `Send + Sync + 'static`,
/// 服务器会为每个请求并发调用本类型的方法。
pub struct WinFsBackend {
    /// 共享根目录(沙箱边界),所有 SMB 路径都会拼接到它下面。
    root: PathBuf,
}

impl WinFsBackend {
    /// 创建后端。如果根目录不存在会自动创建。
    pub fn new(root: impl Into<PathBuf>) -> io::Result<Self> {
        let root = root.into();
        fs::create_dir_all(&root)?;
        Ok(Self { root })
    }

    /// 把已验证的 SMB 相对路径(组件数组)安全地映射为宿主机绝对路径。
    ///
    /// 由于 `SmbPath` 在协议层已经过滤掉 `..`、绝对路径、非法字符,
    /// 这里只需按顺序 `push` 组件即可,不会逃出 root 沙箱。
    fn host_path(&self, path: &SmbPath) -> PathBuf {
        let mut p = self.root.clone();
        for c in path.components() {
            p.push(c);
        }
        p
    }
}

/// 实现库定义的 `ShareBackend` trait(存储抽象层)。
#[async_trait]
impl ShareBackend for WinFsBackend {
    /// 打开(或创建)文件/目录,返回一个可操作的句柄。
    ///
    /// `opts` 是协议层翻译好的 SMB CREATE 意图:
    /// - `opts.directory`    —— 请求的是目录
    /// - `opts.non_directory`—— 请求的是普通文件(目标若是目录应报错)
    /// - `opts.intent`       —— FILE_OPEN / FILE_CREATE / FILE_OPEN_IF /
    ///                          FILE_OVERWRITE_IF / FILE_OVERWRITE 五种处置
    /// - `opts.delete_on_close` —— 关闭句柄时删除文件(SMB 的 FILE_DELETE_ON_CLOSE)
    async fn open(&self, path: &SmbPath, opts: OpenOptions) -> SmbResult<Box<dyn Handle>> {
        let host = self.host_path(path);

        // ------------------------- 目录分支 -------------------------
        if opts.directory {
            // 关键:Windows 上打开目录句柄必须带 BACKUP_SEMANTICS 标志,
            // 否则 CreateFileW 直接返回 AccessDenied(这就是之前
            // "smbclient ls 报 NT_STATUS_ACCESS_DENIED" 的根因)。
            const FILE_FLAG_BACKUP_SEMANTICS: u32 = 0x0200_0000;

            let meta = fs::metadata(&host);
            match meta {
                // 目标是普通文件却要求按目录打开
                Ok(m) if m.is_file() => return Err(SmbError::NotADirectory),
                Ok(_) => {}
                // 目标不存在:只有 Create / OpenOrCreate 允许新建目录
                Err(e) if e.kind() == io::ErrorKind::NotFound => {
                    if !matches!(opts.intent, OpenIntent::Create | OpenIntent::OpenOrCreate) {
                        return Err(SmbError::NotFound);
                    }
                    fs::create_dir(&host).map_err(map_io)?;
                }
                Err(e) => return Err(map_io(e)),
            }

            // 用带 BACKUP_SEMANTICS 的 OpenOptions 打开目录句柄
            // (目录句柄用于 stat / set_times,列目录实际走 fs::read_dir)。
            let file = fs::OpenOptions::new()
                .read(true)
                .custom_flags(FILE_FLAG_BACKUP_SEMANTICS)
                .open(&host)
                .map_err(map_io)?;
            return Ok(Box::new(WinHandle {
                file: Mutex::new(file),
                path: host,
                is_dir: true,
                delete_on_close: opts.delete_on_close,
            }));
        }

        // 普通文件分支:若要求"非目录"但目标恰是目录 → 报错
        if opts.non_directory && fs::metadata(&host).map(|m| m.is_dir()).unwrap_or(false) {
            return Err(SmbError::IsDirectory);
        }

        // 按 SMB CREATE 意图翻译成 Rust 的 OpenOptions 组合
        let mut fo = FsOpenOptions::new();
        fo.read(opts.read).write(opts.write);
        match opts.intent {
            // FILE_OPEN —— 只打开已存在的文件,不存在则报 NotFound
            OpenIntent::Open => {
                if fs::metadata(&host).map(|m| m.is_dir()).unwrap_or(false) {
                    return Err(SmbError::IsDirectory);
                }
                fo.truncate(false).create(false);
            }
            // FILE_CREATE —— 只新建;已存在报 Exists(OBJECT_NAME_COLLISION)
            OpenIntent::Create => {
                if fs::metadata(&host).is_ok() {
                    return Err(SmbError::Exists);
                }
                fo.truncate(false).create_new(true);
            }
            // FILE_OPEN_IF —— 存在则打开,不存在则新建(不截断)
            OpenIntent::OpenOrCreate => {
                fo.truncate(false).create(true);
            }
            // FILE_OVERWRITE_IF —— 存在则打开并清空,不存在则新建
            OpenIntent::OverwriteOrCreate => {
                fo.truncate(true).create(true);
            }
            // FILE_OVERWRITE —— 打开已存在文件并清空;不存在报 NotFound
            OpenIntent::Truncate => {
                if fs::metadata(&host).map_err(map_io)?.is_dir() {
                    return Err(SmbError::IsDirectory);
                }
                fo.truncate(true).create(false);
            }
        }

        let file = fo.open(&host).map_err(map_io)?;
        Ok(Box::new(WinHandle {
            file: Mutex::new(file),
            path: host,
            is_dir: false,
            delete_on_close: opts.delete_on_close,
        }))
    }

    /// 删除文件(SMB DELETE / 目录删除共用)。
    ///
    /// Windows 上对目录执行 `remove_file` 会得到 PermissionDenied,
    /// 借此机会转而尝试 `remove_dir`,并区分"非空目录"报 NotEmpty。
    async fn unlink(&self, path: &SmbPath) -> SmbResult<()> {
        let host = self.host_path(path);
        match fs::remove_file(&host) {
            Ok(()) => Ok(()),
            Err(e) if e.kind() == io::ErrorKind::NotFound => Err(SmbError::NotFound),
            // 目标可能是目录:换 remove_dir 再试
            Err(e) if e.kind() == io::ErrorKind::PermissionDenied => {
                match fs::remove_dir(&host) {
                    Ok(()) => Ok(()),
                    Err(e2) if e2.kind() == io::ErrorKind::DirectoryNotEmpty => {
                        Err(SmbError::NotEmpty) // 目录非空 → STATUS_DIRECTORY_NOT_EMPTY
                    }
                    Err(e2) if e2.kind() == io::ErrorKind::NotFound => Err(SmbError::NotFound),
                    Err(e2) => Err(map_io(e2)),
                }
            }
            Err(e) => Err(map_io(e)),
        }
    }

    /// 重命名(SMB RENAME)。协议层要求:目标已存在时必须拒绝。
    async fn rename(&self, from: &SmbPath, to: &SmbPath) -> SmbResult<()> {
        let host_from = self.host_path(from);
        let host_to = self.host_path(to);
        // 目标存在 → Exists(Windows 上 rename 覆盖目标会失败且行为隐蔽,
        // 所以先显式检查,保证 NTSTATUS 语义正确)
        if fs::metadata(&host_to).is_ok() {
            return Err(SmbError::Exists);
        }
        fs::rename(&host_from, &host_to).map_err(map_io)
    }

    /// 静态能力声明,协议层在 TREE_CONNECT 时读取。
    fn capabilities(&self) -> BackendCapabilities {
        BackendCapabilities {
            is_read_only: false,  // 本后端可写
            case_sensitive: false, // 走 Windows NTFS,大小写不敏感
        }
    }
}

// ============================================================================
// 文件句柄:WinHandle
// ============================================================================

/// 一个已打开的文件或目录句柄,对应一次 SMB CREATE。
///
/// - `file`:底层 Windows 文件句柄;用 Mutex 包起来以配合 trait 的
///   `&self` 签名(内部可变性),并发读写串行化。
/// - `path`:宿主磁盘路径,供 stat / list_dir / delete_on_close 使用。
/// - `is_dir`:区分文件/目录,协议层不允许对目录读写数据。
/// - `delete_on_close`:SMB FILE_DELETE_ON_CLOSE,close() 时删除本体。
pub struct WinHandle {
    file: Mutex<fs::File>,
    path: PathBuf,
    is_dir: bool,
    delete_on_close: bool,
}

/// 把 Rust 的 io::Error 映射为库的 SmbError(NTSTATUS 语义)。
///
/// 常见 io 错误码直接映射到对应的 SMB 错误;未识别的保留为 Io
/// (STATUS_UNEXPECTED_IO_ERROR)。
fn map_io(e: io::Error) -> SmbError {
    match e.kind() {
        io::ErrorKind::NotFound => SmbError::NotFound,             // STATUS_OBJECT_NAME_NOT_FOUND
        io::ErrorKind::PermissionDenied => SmbError::AccessDenied, // STATUS_ACCESS_DENIED
        io::ErrorKind::AlreadyExists => SmbError::Exists,          // STATUS_OBJECT_NAME_COLLISION
        io::ErrorKind::DirectoryNotEmpty => SmbError::NotEmpty,    // STATUS_DIRECTORY_NOT_EMPTY
        io::ErrorKind::InvalidInput => SmbError::IsDirectory,      // STATUS_FILE_IS_A_DIRECTORY
        _ => SmbError::Io(e),
    }
}

/// 把文件元数据整理成 SMB 的 FileInfo(QUERY_INFO 响应体)。
///
/// Windows 的 `MetadataExt::creation_time()` 等返回的就是 FILETIME
/// (1601-01-01 起 100ns 刻度),恰好与 SMB 的 FILETIME 格式一致,无需转换。
/// `change_time`(变更时间)Windows 不提供,退化为最后写入时间。
fn file_info(name: String, meta: &fs::Metadata) -> FileInfo {
    FileInfo {
        name,                     // 显示名(最后一个路径组件)
        end_of_file: meta.len(),  // 文件大小(字节)
        allocation_size: meta.len(), // 分配大小(简化:与文件大小相同)
        creation_time: meta.creation_time(),      // FILETIME:创建时间
        last_access_time: meta.last_access_time(),// FILETIME:最后访问
        last_write_time: meta.last_write_time(),  // FILETIME:最后写入
        change_time: meta.last_write_time(),      // FILETIME:变更时间(近似)
        is_directory: meta.is_dir(),
        file_index: 0, // 无唯一文件索引时返回 0,协议层会用 FileId 代替
    }
}

/// FILETIME(u64,1601-01-01 起 100ns 刻度) → std SystemTime。
///
/// 换算:Unix 纪元(1970-01-01)对应的 FILETIME 刻度为 116444736000000000。
fn filetime_to_system_time(ft: u64) -> SystemTime {
    const EPOCH_DIFF_TICKS: u64 = 116_444_736_000_000_000;
    let nanos = ft.saturating_sub(EPOCH_DIFF_TICKS) * 100; // 刻度 → 纳秒
    UNIX_EPOCH + Duration::from_nanos(nanos)
}

/// 实现库定义的 `Handle` trait(一次打开的文件/目录)。
#[async_trait]
impl Handle for WinHandle {
    /// 按偏移读取(SMB READ)。
    ///
    /// `seek_read` 是 Windows 定位读:不改变文件游标,天然支持并发
    /// 多句柄按偏移访问;返回实际读到的字节数(文件尾部可能少于请求)。
    async fn read(&self, offset: u64, len: u32) -> SmbResult<Bytes> {
        if self.is_dir {
            return Err(SmbError::IsDirectory);
        }
        let mut buf = vec![0u8; len as usize];
        let n = self
            .file
            .lock()
            .map_err(|_| SmbError::Io(io::Error::other("lock poisoned")))?
            .seek_read(&mut buf, offset)
            .map_err(map_io)?;
        buf.truncate(n); // 裁掉未读满的部分(读满则无影响)
        Ok(Bytes::from(buf))
    }

    /// 按偏移写入(SMB WRITE),返回实际写入字节数。
    async fn write(&self, offset: u64, data: &[u8]) -> SmbResult<u32> {
        if self.is_dir {
            return Err(SmbError::IsDirectory);
        }
        let n = self
            .file
            .lock()
            .map_err(|_| SmbError::Io(io::Error::other("lock poisoned")))?
            .seek_write(data, offset) // Windows 定位写,同上不移动游标
            .map_err(map_io)?;
        Ok(n as u32)
    }

    /// 冲刷缓冲(SMB FLUSH)。std File 无用户态缓冲,数据已交给系统,
    /// flush 即把文件缓冲刷到磁盘。这里通过 `Write` trait 调用。
    async fn flush(&self) -> SmbResult<()> {
        self.file
            .lock()
            .map_err(|_| SmbError::Io(io::Error::other("lock poisoned")))?
            .flush()
            .map_err(map_io)
    }

    /// 查询文件信息(SMB QUERY_INFO,不带路径时用句柄对应文件)。
    async fn stat(&self) -> SmbResult<FileInfo> {
        let meta = fs::metadata(&self.path).map_err(map_io)?;
        // 显示名取最后一个路径组件;根目录(共享根)取整个路径
        let name = self
            .path
            .file_name()
            .map(|n| n.to_string_lossy().into_owned())
            .unwrap_or_else(|| self.path.to_string_lossy().into_owned());
        Ok(file_info(name, &meta))
    }

    /// 设置时间戳(SMB SET_INFO / FILE_BASIC_INFORMATION)。
    ///
    /// SMB 侧给的是 FILETIME,先转 SystemTime,再逐项设置;
    /// `None` 表示该项不修改。change_time Windows 不支持,忽略。
    async fn set_times(&self, times: FileTimes) -> SmbResult<()> {
        let mut wt = std::fs::FileTimes::new();
        if let Some(t) = times.creation_time {
            wt = wt.set_created(filetime_to_system_time(t));
        }
        if let Some(t) = times.last_access_time {
            wt = wt.set_accessed(filetime_to_system_time(t));
        }
        if let Some(t) = times.last_write_time {
            wt = wt.set_modified(filetime_to_system_time(t));
        }
        // 只有确实要修改时才调用(FileTimes 全空时调用是无操作)
        if times.creation_time.is_some()
            || times.last_access_time.is_some()
            || times.last_write_time.is_some()
        {
            self.file
                .lock()
                .map_err(|_| SmbError::Io(io::Error::other("lock poisoned")))?
                .set_times(wt)
                .map_err(map_io)?;
        }
        Ok(())
    }

    /// 截断/扩展文件到指定长度(SMB SET_END_OF_FILE)。
    async fn truncate(&self, len: u64) -> SmbResult<()> {
        if self.is_dir {
            return Err(SmbError::IsDirectory);
        }
        self.file
            .lock()
            .map_err(|_| SmbError::Io(io::Error::other("lock poisoned")))?
            .set_len(len)
            .map_err(map_io)
    }

    /// 列出目录内容(SMB QUERY_DIRECTORY)。
    ///
    /// `pattern`(通配符)本后端不实现匹配,返回全部条目;
    /// 协议层 dispatcher 会自行按 pattern 后过滤。
    async fn list_dir(&self, _pattern: Option<&str>) -> SmbResult<Vec<DirEntry>> {
        if !self.is_dir {
            return Err(SmbError::NotADirectory);
        }
        let mut entries = Vec::new();
        for entry in fs::read_dir(&self.path).map_err(map_io)? {
            let entry = entry.map_err(map_io)?;
            let meta = entry.metadata().map_err(map_io)?;
            entries.push(DirEntry {
                info: file_info(entry.file_name().to_string_lossy().into_owned(), &meta),
            });
        }
        Ok(entries)
    }

    /// 关闭句柄(SMB CLOSE)。
    ///
    /// 先冲刷一次确保数据落盘;若该句柄带 FILE_DELETE_ON_CLOSE,
    /// 则同时删除文件(或目录)。
    async fn close(self: Box<Self>) -> SmbResult<()> {
        if let Ok(mut f) = self.file.lock() {
            let _ = f.flush();
        }
        if self.delete_on_close {
            let p = self.path.clone();
            // 先按文件删,失败再按目录删(与 unlink 同样的容错思路)
            let _ = fs::remove_file(&p);
            let _ = fs::remove_dir(&p);
        }
        Ok(())
    }
}
