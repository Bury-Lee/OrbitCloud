//! ============================================================================
//! core/enter.rs —— 全局配置结构定义(与 SMB.yaml 字段一一对应)
//! ============================================================================
//!
//! 设计来源(草稿扩展):`struct Core { Host, Des, log }` ——
//! - `Host` 监听地址 → 本文件 `SmbConfig.listen`(SMB 监听);
//! - `Des` 发送往地址(后端服务的位置)→ `GatewayConfig.addr`(Go 网关地址);
//! - `log` 日志配置 → `LogConfig.level`。
//!
//! 命名规范:Rust snake_case;字段均附"意义"注释。
//! 解析/校验见 flag 模块(读取与反序列化),本模块只承载类型定义。

use serde::Deserialize; // 反序列化 derive(配置解析)

/// 全局配置根(对应 SMB.yaml 顶层三个分节)。
///
/// 承载功能:启动期经 `flag::load_config` 反序列化填充;
/// 运行期只读访问(不提供热重载,变更走 Go 网关推送)。
#[derive(Debug, Clone, Deserialize)] // 调试/克隆/反序列化派生
pub struct Config {
    /// SMB 服务配置(协议接入侧)。
    pub smb: SmbConfig,
    /// Go 网关配置(私有 TCP 连接侧)。
    pub gateway: GatewayConfig,
    /// 日志配置。
    pub log: LogConfig,
}

/// SMB 服务配置(对应 SMB.yaml `smb:` 分节)。
///
/// 承载功能:决定本进程 SMB 监听行为与共享网络标识。
#[derive(Debug, Clone, Deserialize)] // 派生
pub struct SmbConfig {
    /// 监听地址(如 "0.0.0.0:2445";对应草稿 Host 监听地址)。
    pub listen: String,
    /// NetBIOS 名(局域网浏览显示名;可选,缺省用默认值)。
    pub netbios_name: String,
}

/// Go 网关配置(对应 SMB.yaml `gateway:` 分节)。
///
/// 承载功能:定位 Go 网关并约定保活/同步节奏(帧协议见 types.rs);
/// 缓冲与并发参数与 Go 侧请求池对齐(设计点 6)。
#[derive(Debug, Clone, Deserialize)] // 派生
pub struct GatewayConfig {
    /// Go 网关地址(如 "127.0.0.1:9001";对应草稿 Des 发送往地址,后端服务位置)。
    pub addr: String,
    /// 共享密钥(可直接配置在 SMB.yaml;优先级高于 shared_key_env。
    /// 留空时回退到 shared_key_env 指定的环境变量;两者皆空则跳过静态鉴权,
    /// 等待握手后动态密钥协商(接口后续实现)。
    #[serde(default)]
    pub shared_key: String,
    /// 共享密钥所在环境变量名(shared_key 为空时读取;留空 = 跳过静态密钥)。
    pub shared_key_env: String,
    /// 心跳间隔(秒;0 = 禁用心跳)。
    pub heartbeat_secs: u64,
    /// 与 Go 网关全量对账周期(秒;增量推送丢失时的兜底)。
    pub sync_interval_secs: u64,
    /// 发送管道缓冲长度(设计点 6):Rust 侧帧写入先进该容量的缓冲管道,
    /// 由 writer 任务消费写 socket;与 Go 侧请求池队列深度对齐,取值建议
    /// 2 的幂(如 1024)。
    pub channel_buffer: usize,
    /// RPC 请求应答超时(秒;缺省 30)。大文件 Flush/Close 涉及写回缓存
    /// 整体上传对象存储,耗时可超 30s——按文件大小/网络调整,避免大文件
    /// 保存被误判超时(客户端已写入但显示失败)。
    #[serde(default = "default_rpc_timeout_secs")]
    pub rpc_timeout_secs: u64,
}

/// 缺省 RPC 超时(秒;与旧行为一致)。
fn default_rpc_timeout_secs() -> u64 {
    30
}

/// 日志配置(对应 SMB.yaml `log:` 分节)。
///
/// 承载功能:初始化本网关日志器(与 Go 侧 orbitcloud/log 对齐):
/// - 级别过滤(仍可用 RUST_LOG 环境变量覆盖);
/// - 文本文件日志:按日期建子文件夹、启动时间命名,可配置开关;
/// - 本地 SQLite 日志入库 + 定时清理过期记录(保留天数可配置)。
#[derive(Debug, Clone, Deserialize)] // 派生
pub struct LogConfig {
    /// 日志级别(debug/info/warn/error)。
    pub level: String,
    /// 文本日志输出文件夹(相对工作目录;空 = 关闭文本文件日志,仅控制台)。
    pub output: String,
    /// 文本日志文件名前缀(缺省 "smb-gateway";每日一个子文件夹,
    /// 文件名 = {file_prefix}-{HH-MM-SS}.log,每次启动以启动时刻命名)。
    #[serde(default = "default_file_prefix")]
    pub file_prefix: String,
    /// 是否启用本地 SQLite 日志入库(缺省 true)。
    #[serde(default = "default_true")]
    pub db_enabled: bool,
    /// SQLite 数据库文件路径(缺省 "{output}/logs.db";output 为空时
    /// 退化为 "./logs/logs.db")。
    #[serde(default)]
    pub db_path: String,
    /// 入库最低级别(debug/info/warn/error;缺省 "info",低于该级别不入库)。
    #[serde(default = "default_db_level")]
    pub db_level: String,
    /// SQLite 日志保留天数(定时任务清理更早记录;缺省 30)。
    #[serde(default = "default_retention_days")]
    pub retention_days: u64,
    /// 过期日志清理周期(秒;缺省 3600,下限 60)。
    #[serde(default = "default_cleanup_interval_secs")]
    pub cleanup_interval_secs: u64,
}

/// 缺省文本日志文件名前缀。
fn default_file_prefix() -> String {
    "smb-gateway".into()
}

/// 缺省 true(serde default 助手)。
fn default_true() -> bool {
    true
}

/// 缺省入库最低级别(serde default 助手)。
fn default_db_level() -> String {
    "info".into()
}

/// 缺省 SQLite 保留天数(serde default 助手)。
fn default_retention_days() -> u64 {
    30
}

/// 缺省清理周期(秒;serde default 助手)。
fn default_cleanup_interval_secs() -> u64 {
    3600
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

/// 共享密钥默认环境变量名(与 Go 侧 config 默认一致)。
pub const DEFAULT_KEY_ENV: &str = "ORBITCLOUD_SMB_GATEWAY_KEY";

impl Config {
    /// 构造配置:校验完整性后返回(日志初始化由 main 负责,避免依赖顺序问题)。
    ///
    /// 参数:
    /// - `config`:已加载的全局配置(见 flag::load_config)。
    ///
    /// 返回值:配置自身(校验失败由调用方在启动时终止)。
    pub fn new(config: Config) -> Self {
        config // 透传构造:目前无额外初始化;后续校验/默认值填充可在此扩展
    }
}
