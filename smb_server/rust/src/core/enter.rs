//! ============================================================================
//! core/enter.rs —— 全局配置结构定义(与 config.yaml 字段一一对应)
//! ============================================================================
//!
//! 设计来源(草稿扩展):`struct Core { Host, Des, log }` ——
//! - `Host` 监听地址 → 本文件 `SmbConfig.listen`(SMB 监听);
//! - `Des` 发送往地址(后端服务的位置)→ `GatewayConfig.addr`(Go 网关地址);
//! - `log` 日志配置 → `LogConfig.level`。
//!
//! 命名规范:Rust snake_case;字段均附"意义"注释。
//! 解析/校验见 flag 模块(读取与反序列化),本模块只承载类型定义。

use serde::Deserialize;

/// 全局配置根(对应 config.yaml 顶层三个分节)。
///
/// 承载功能:启动期经 `flag::load_config` 反序列化填充;
/// 运行期只读访问(不提供热重载,变更走 Go 网关推送)。
#[derive(Debug, Clone, Deserialize)]
pub struct Config {
    /// SMB 服务配置(协议接入侧)。
    pub smb: SmbConfig,
    /// Go 网关配置(私有 TCP 连接侧)。
    pub gateway: GatewayConfig,
    /// 日志配置。
    pub log: LogConfig,
}

/// SMB 服务配置(对应 config.yaml `smb:` 分节)。
///
/// 承载功能:决定本进程 SMB 监听行为与共享网络标识。
#[derive(Debug, Clone, Deserialize)]
pub struct SmbConfig {
    /// 监听地址(如 "0.0.0.0:2445";对应草稿 Host 监听地址)。
    pub listen: String,
    /// NetBIOS 名(局域网浏览显示名;可选,缺省用默认值)。
    pub netbios_name: String,
}

/// Go 网关配置(对应 config.yaml `gateway:` 分节)。
///
/// 承载功能:定位 Go 网关并约定保活/同步节奏(帧协议见 types.rs)。
#[derive(Debug, Clone, Deserialize)]
pub struct GatewayConfig {
    /// Go 网关地址(如 "127.0.0.1:9001";对应草稿 Des 发送往地址,后端服务位置)。
    pub addr: String,
    /// 共享密钥所在环境变量名(密钥本身禁止写入 config.yaml)。
    pub shared_key_env: String,
    /// 心跳间隔(秒;0 = 禁用心跳)。
    pub heartbeat_secs: u64,
    /// 与 Go 网关全量对账周期(秒;增量推送丢失时的兜底)。
    pub sync_interval_secs: u64,
}

/// 日志配置(对应 config.yaml `log:` 分节)。
///
/// 承载功能:初始化 tracing 的默认级别(仍可用 RUST_LOG 环境变量覆盖)。
#[derive(Debug, Clone, Deserialize)]
pub struct LogConfig {
    /// 日志级别(debug/info/warn/error)。
    pub level: String,
}

/// 运行期全局单例(仿 Go 侧 core.Storage 等单例的 Rust 对应)。
///
/// 承载功能:启动完成后持有"配置 + 已建立连接 + 句柄表"的组合体,
/// 供各模块直接访问;此处只定义形态,构造过程见 main.rs 与各模块。
pub struct Core {
    /// 已加载配置(只读;解析见 flag 模块)。
    pub config: Config,
    /// 到 Go 网关的连接(建立流程见 remote_backend::GatewayClient::connect)。
    pub gateway_conn: Option<()>, // 伪代码:真实现为 Arc<GatewayClient>
    /// 远程句柄表(Go 网关句柄的本地存根表;见 remote_backend::RemoteHandle)。
    pub handle_table: Option<()>, // 伪代码:真实现为句柄注册表
}

impl Core {
    /// 构造单例(伪代码阶段:仅占位)。
    ///
    /// 参数:
    /// - `config`:已加载的全局配置。
    ///
    /// 返回值:组装完成的单例(连接/句柄表为空,由启动流程填充)。
    ///
    /// 内部逻辑(伪代码):
    /// 1. 校验配置完整性(必填项非空,见 flag::validate);
    /// 2. 初始化日志(按 config.log.level);
    /// 3. 返回 Core{config, gateway_conn: None, handle_table: None}。
    pub fn new(config: Config) -> Self {
        let _ = config;
        todo!("伪代码:见上方分步注释")
    }
}
