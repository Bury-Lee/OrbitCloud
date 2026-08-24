//! ============================================================================
//! log/mod.rs —— 网关日志模块(与 Go 侧 orbitcloud/log 对齐)
//! ============================================================================
//!
//! 输出格式(与 Go 侧一致):
//! ```text
//! [2026-08-23 15:04:05.123] [INFO] [tag1] [tag2] 消息内容
//! ```
//!
//! 模块索引:
//! - 本文件:级别(Level)、全局日志器(Logger/init)、写入核心(emit)、
//!   前缀标签宏(debugf!/infof!/warnf!/errorf!);
//! - `file_writer`:文本文件按日期轮转(每日子文件夹 + 启动时刻命名,
//!   与 Go 侧 rotate.go 对齐);
//! - `sqlite`:本地 SQLite 日志入库(独立写线程)+ 定时清理过期记录(可配置);
//! - `ixr_layer`:ixr-smb-server(vendor 库)内部 tracing 日志统一接入
//!   同一文件/控制台/SQLite。
//!
//! 调用方式与 Go 侧 DebugfP/InfofP(format, prefix, args...) 对齐:
//! ```rust,ignore
//! infof!("连接 {}", &["网关", "握手"], addr);
//! ```

mod file_writer; // 文本文件每日轮转(与 Go 侧 rotate.go 对齐)
mod ixr_layer; // vendor 库(tracing)日志接入
mod sqlite; // 本地 SQLite 入库 + 定时清理

use std::fmt; // 格式化参数(fmt::Arguments)
use std::io::Write; // 控制台写入原语
use std::sync::mpsc; // SQLite 写线程通道类型
use std::sync::{Mutex, OnceLock}; // 全局单例

use crate::core::enter::LogConfig; // 日志配置

use self::file_writer::DailyFile; // 文本文件 writer(每日轮转)
use self::sqlite::{sqlite_start, spawn_cleanup, DbMsg, LogRecord}; // SQLite 入库组件

// ============================================================================
// 级别
// ============================================================================

/// 日志级别(与 Go 侧 log.Level 对齐;Debug < Info < Warn < Error)。
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub enum Level {
    /// 调试(仅开发环境)
    Debug,
    /// 信息
    Info,
    /// 警告
    Warn,
    /// 错误
    Error,
}

impl Level {
    /// 级别展示串(与 Go 侧 Level.String() 对齐)。
    pub fn as_str(self) -> &'static str {
        match self {
            Level::Debug => "DEBUG",
            Level::Info => "INFO",
            Level::Warn => "WARN",
            Level::Error => "ERROR",
        }
    }

    /// 从配置字符串解析(debug/info/warn/error,忽略大小写与空白)。
    pub fn parse(s: &str) -> Option<Level> {
        match s.trim().to_ascii_lowercase().as_str() {
            "debug" => Some(Level::Debug),
            "info" => Some(Level::Info),
            "warn" => Some(Level::Warn),
            "error" => Some(Level::Error),
            _ => None,
        }
    }
}

// ============================================================================
// 全局单例与公开 API
// ============================================================================

/// 全局日志器(init 一次性初始化;未初始化时 emit 退化为仅控制台输出)。
struct Logger {
    /// 最低输出级别(低于该级别的调用直接跳过)。
    min_level: Level,
    /// 入库最低级别(低于该级别不写 SQLite)。
    db_min_level: Level,
    /// 文本文件 writer(None = 关闭文本文件日志,仅控制台)。
    file: Mutex<Option<DailyFile>>,
    /// SQLite 写线程通道发送端(None = 未启用入库)。
    db_tx: Mutex<Option<mpsc::Sender<DbMsg>>>,
}

static LOGGER: OnceLock<Logger> = OnceLock::new();

/// 初始化全局日志器(启动期调用一次;失败返回错误串,调用方终止启动)。
///
/// 参数:`cfg` 已解析的日志配置(config.yaml `log:` 分节)。
///
/// 步骤:
/// 1. 级别:RUST_LOG 环境变量优先,其次配置,非法退化为 Info;
/// 2. 文本文件日志(output 空 = 关闭,仅控制台);
/// 3. SQLite 入库(独立线程串行写,调用方不阻塞);
/// 4. vendor 库(tracing)日志接入 + 定时清理任务。
pub fn init(cfg: &LogConfig) -> Result<(), String> {
    let min_level = Level::parse(&std::env::var("RUST_LOG").unwrap_or_default())
        .or_else(|| Level::parse(&cfg.level))
        .unwrap_or(Level::Info);
    let db_min_level = Level::parse(&cfg.db_level).unwrap_or(Level::Info);

    let file = if cfg.output.trim().is_empty() {
        None // output 空 = 关闭文本文件日志,仅控制台
    } else {
        Some(DailyFile::open(cfg.output.trim(), &cfg.file_prefix)?)
    };

    let db_tx = if cfg.db_enabled {
        Some(sqlite_start(cfg)?)
    } else {
        None
    };

    if LOGGER
        .set(Logger {
            min_level,
            db_min_level,
            file: Mutex::new(file),
            db_tx: Mutex::new(db_tx.clone()),
        })
        .is_err()
    {
        return Err("log: 重复初始化".into());
    }

    // vendor 库(tracing)日志统一接入文件/控制台/SQLite
    ixr_layer::install(&cfg, db_tx.as_ref(), db_min_level);

    // 定时清理 SQLite 过期记录(可配置保留天数/周期)
    if let Some(tx) = db_tx {
        spawn_cleanup(tx, cfg.retention_days.max(1), cfg.cleanup_interval_secs.max(60));
    }

    emit(
        Level::Info,
        &["日志"],
        format_args!(
            "已初始化 级别={} 文本文件={} SQLite={}",
            min_level.as_str(),
            if LOGGER.get().and_then(|l| l.file.lock().ok()).map_or(false, |f| f.is_some()) {
                "开"
            } else {
                "关"
            },
            if LOGGER.get().and_then(|l| l.db_tx.lock().ok()).map_or(false, |t| t.is_some()) {
                "开"
            } else {
                "关"
            }
        ),
    );
    Ok(())
}

// ============================================================================
// 日志写入核心
// ============================================================================

/// 写一条日志(与 Go 侧 f 变体对齐的底层入口)。
///
/// 参数:
/// - `level`:级别(低于全局最低级别直接跳过);
/// - `tags`:前缀标签列表(渲染为 `[tag1] [tag2] ` 标记,空列表不带标记);
/// - `args`:消息与格式化参数(format_args! 产物)。
///
/// 输出:控制台 + 文本文件(开启时) + SQLite(开启且达到入库级别时);
/// 写失败仅丢弃,不阻断调用方(与 Go 侧返回 error 但调用方普遍忽略一致)。
pub fn emit(level: Level, tags: &[&str], args: fmt::Arguments) {
    let msg = std::fmt::format(args); // 格式化消息文本
    let logger = LOGGER.get(); // 未初始化:仅控制台输出
    if let Some(l) = logger {
        if level < l.min_level {
            return; // 低于最低级别:直接跳过(不输出、不入库)
        }
    }

    let now = chrono::Local::now(); // 本地时间(毫秒精度)
    let line = format!(
        "[{}] [{}] {}{}\n", // 与 Go 侧 output() 格式一致
        now.format("%Y-%m-%d %H:%M:%S%.3f"), // 时间
        level.as_str(), // 级别
        render_tags(tags), // [tag1] [tag2]
        msg // 消息内容
    );

    // 控制台输出(与 Go 侧 outWriter() 默认 os.Stdout 对齐)
    let _ = std::io::stdout().write_all(line.as_bytes());

    if let Some(l) = logger {
        write_file_line(&line); // 文本文件输出(开启时;DailyFile 内部跨天轮转)
        persist_sqlite(l, level, tags, &now, msg); // SQLite 入库(开启且达到入库级别时)
    }
}

/// 写一行到文本日志文件(输出开关关闭时为空操作)。
/// `pub(crate)`:ixr_layer 的写入器复用(文件与页面输出共享同一轮转句柄)。
pub(crate) fn write_file_line(line: &str) {
    if let Some(l) = LOGGER.get() {
        if let Ok(mut f) = l.file.lock() {
            if let Some(daily) = f.as_mut() {
                let _ = daily.write(line.as_bytes());
            }
        }
    }
}

/// 按入库级别过滤后投递 SQLite 写线程(通道满/关闭仅丢弃)。
fn persist_sqlite(l: &Logger, level: Level, tags: &[&str], now: &chrono::DateTime<chrono::Local>, msg: String) {
    if level < l.db_min_level {
        return; // 低于入库最低级别:不写 SQLite
    }
    if let Ok(tx) = l.db_tx.lock() {
        if let Some(tx) = tx.as_ref() {
            let _ = tx.send(DbMsg::Insert(LogRecord {
                time: now.format("%Y-%m-%d %H:%M:%S%.3f").to_string(),
                level: level.as_str().to_string(),
                tags: render_tags(tags).trim_end().to_string(),
                message: msg,
                created_at: now.timestamp(),
            }));
        }
    }
}

/// 渲染标签列表:[tag1, tag2] → "[tag1] [tag2] "(空列表/空标签 → 空串)。
fn render_tags(tags: &[&str]) -> String {
    let mut s = String::new();
    for t in tags {
        if t.trim().is_empty() {
            continue;
        }
        s.push('[');
        s.push_str(t);
        s.push_str("] ");
    }
    s
}

// ============================================================================
// 宏:与 Go 侧 DebugfP/InfofP/WarnfP/ErrorfP(format, prefix, args...) 对齐
// ============================================================================

/// 带前缀标签的 Debug 日志:`debugf!("连接 {}", &["网关"], addr)`。
macro_rules! debugf {
    ($fmt:literal, $prefix:expr $(, $arg:expr)* $(,)?) => {
        $crate::log::emit($crate::log::Level::Debug, $prefix, format_args!($fmt $(, $arg)*))
    };
}

/// 带前缀标签的 Info 日志:`infof!("连接 {}", &["网关"], addr)`。
macro_rules! infof {
    ($fmt:literal, $prefix:expr $(, $arg:expr)* $(,)?) => {
        $crate::log::emit($crate::log::Level::Info, $prefix, format_args!($fmt $(, $arg)*))
    };
}

/// 带前缀标签的 Warn 日志:`warnf!("连接 {}", &["网关"], addr)`。
macro_rules! warnf {
    ($fmt:literal, $prefix:expr $(, $arg:expr)* $(,)?) => {
        $crate::log::emit($crate::log::Level::Warn, $prefix, format_args!($fmt $(, $arg)*))
    };
}

/// 带前缀标签的 Error 日志:`errorf!("连接 {}", &["网关"], addr)`。
macro_rules! errorf {
    ($fmt:literal, $prefix:expr $(, $arg:expr)* $(,)?) => {
        $crate::log::emit($crate::log::Level::Error, $prefix, format_args!($fmt $(, $arg)*))
    };
}

// (宏经 main.rs 的 #[macro_use] mod log; 注入 crate 作用域,无需额外导出)

// ============================================================================
// 单元测试
// ============================================================================

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn level_parse_and_str() {
        for (s, want) in [
            ("debug", Level::Debug),
            ("INFO", Level::Info),
            (" warn ", Level::Warn),
            ("Error", Level::Error),
        ] {
            assert_eq!(Level::parse(s), Some(want), "parse {s:?}");
        }
        assert_eq!(Level::Debug.as_str(), "DEBUG");
        assert_eq!(Level::Info.as_str(), "INFO");
        assert_eq!(Level::Warn.as_str(), "WARN");
        assert_eq!(Level::Error.as_str(), "ERROR");
        assert_eq!(Level::parse("verbose"), None);
        assert_eq!(Level::parse(""), None);
        assert!(Level::Debug < Level::Info && Level::Info < Level::Warn && Level::Warn < Level::Error);
    }

    #[test]
    fn render_tags_skips_empty() {
        assert_eq!(render_tags(&[]), "");
        assert_eq!(render_tags(&["gorm"]), "[gorm] ");
        assert_eq!(render_tags(&["gorm", "sql"]), "[gorm] [sql] ");
        assert_eq!(render_tags(&["a", "", "b", "  "]), "[a] [b] ");
    }

    #[test]
    fn emit_before_init_goes_to_stdout() {
        // 未初始化:emit 不 panic,仅控制台(此处无法断言,只验证不崩溃)
        emit(Level::Info, &["t"], format_args!("hello"));
    }
}
