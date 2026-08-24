//! ============================================================================
//! log/ixr_layer.rs —— ixr-smb-server(vendor 库)内部 tracing 日志接入
//! ============================================================================
//!
//! 本网关自身代码不使用 tracing(见 mod.rs 的前缀标签宏),此层仅兜底
//! vendor 库(ixr-smb-server)内部的 tracing 日志:
//! - `GoStyleFormatter`:Go 风格格式化(与本网关日志格式一致);
//! - `install`:注册订阅器,把 vendor 日志写入 控制台 + 文本文件 + SQLite;
//! - `SqliteLayer`:捕获 (级别, 消息) 投递 SQLite 写线程(级别过滤见配置)。

use std::fmt; // 字段访问器(fmt::Debug / fmt::Write)
use std::io::Write; // IxrLineWriter 的 Write 实现
use std::sync::mpsc; // SQLite 写线程通道

use crate::core::enter::LogConfig; // 日志配置

use super::sqlite::{DbMsg, LogRecord}; // 入库记录
use super::{write_file_line, Level}; // 文件写入转发 + 级别

/// 把 tracing 级别映射到本模块级别(TRACE 归入 Debug,与 Go 侧无 TRACE 对齐)。
fn level_from_tracing(lvl: tracing::Level) -> Level {
    match lvl {
        tracing::Level::TRACE | tracing::Level::DEBUG => Level::Debug,
        tracing::Level::INFO => Level::Info,
        tracing::Level::WARN => Level::Warn,
        tracing::Level::ERROR => Level::Error,
    }
}

// ============================================================================
// Go 风格格式化(与 mod.rs emit 输出格式一致)
// ============================================================================

/// Go 风格事件格式化器:`[2026-08-23 17:05:45.123] [INFO] 消息`,
/// 附加字段按 `键=值` 追加在消息后。
struct GoStyleFormatter;

impl tracing_subscriber::fmt::FormatEvent<
    tracing_subscriber::registry::Registry,
    tracing_subscriber::fmt::format::DefaultFields,
> for GoStyleFormatter
{
    fn format_event(
        &self,
        _ctx: &tracing_subscriber::fmt::FmtContext<
            '_,
            tracing_subscriber::registry::Registry,
            tracing_subscriber::fmt::format::DefaultFields,
        >,
        mut writer: tracing_subscriber::fmt::format::Writer<'_>,
        event: &tracing::Event<'_>,
    ) -> std::fmt::Result {
        use tracing::Level;
        write!(
            writer,
            "[{}] ",
            chrono::Local::now().format("%Y-%m-%d %H:%M:%S%.3f")
        )?;
        let level = match *event.metadata().level() {
            Level::TRACE => "TRACE",
            Level::DEBUG => "DEBUG",
            Level::INFO => "INFO",
            Level::WARN => "WARN",
            Level::ERROR => "ERROR",
        };
        write!(writer, "[{level}] ")?;
        let mut visitor = FieldWriter(&mut writer);
        event.record(&mut visitor);
        writeln!(writer)
    }
}

/// 事件字段写出访问器:message 原样输出,其余字段按 `键=值` 追加。
struct FieldWriter<'w>(&'w mut dyn std::fmt::Write);

impl tracing::field::Visit for FieldWriter<'_> {
    fn record_str(&mut self, field: &tracing::field::Field, value: &str) {
        if field.name() == "message" {
            let _ = write!(self.0, "{value}");
        } else {
            let _ = write!(self.0, " {}=\"{}\"", field.name(), value);
        }
    }
    fn record_debug(&mut self, field: &tracing::field::Field, value: &dyn fmt::Debug) {
        if field.name() == "message" {
            let _ = write!(self.0, "{value:?}");
        } else {
            let _ = write!(self.0, " {}=\"{value:?}\"", field.name());
        }
    }
    fn record_i64(&mut self, field: &tracing::field::Field, value: i64) {
        let _ = write!(self.0, " {}={}", field.name(), value);
    }
    fn record_u64(&mut self, field: &tracing::field::Field, value: u64) {
        let _ = write!(self.0, " {}={}", field.name(), value);
    }
    fn record_bool(&mut self, field: &tracing::field::Field, value: bool) {
        let _ = write!(self.0, " {}={}", field.name(), value);
    }
    fn record_error(&mut self, field: &tracing::field::Field, value: &(dyn std::error::Error + 'static)) {
        let _ = write!(self.0, " {}={}", field.name(), value);
    }
}

// ============================================================================
// 订阅器安装:控制台 + 文本文件 + SQLite
// ============================================================================

/// 安装 tracing 订阅器:把 vendor 库(tracing)日志统一格式化为 Go 风格,
/// 写入 控制台 + 文本文件(与全局共用 DailyFile)+ SQLite(达到入库级别时)。
pub(super) fn install(
    cfg: &LogConfig,
    db_tx: Option<&mpsc::Sender<DbMsg>>,
    db_min_level: Level,
) {
    use tracing_subscriber::layer::SubscriberExt; // with() 组合层
    use tracing_subscriber::util::SubscriberInitExt; // try_init()
    use tracing_subscriber::Layer; // with_filter(过滤器包裹)

    // 过滤器:RUST_LOG 优先,否则用配置级别(仅放行达到级别的 vendor 日志)
    let filter = std::env::var("RUST_LOG").unwrap_or_else(|_| cfg.level.clone());
    let filter = tracing_subscriber::EnvFilter::try_new(&filter)
        .unwrap_or_else(|_| tracing_subscriber::EnvFilter::new("info"));

    // 组合:registry() 为基座 Subscriber,逐层 with() 包裹(Filtered 是 Layer,
    // 不可直接再 with,须先挂到 registry 上再叠下一层)。
    let fmt_layer = tracing_subscriber::fmt::layer()
        .event_format(GoStyleFormatter)
        .with_writer(IxrGoWriter)
        .with_filter(filter);
    let subscriber = tracing_subscriber::registry()
        .with(fmt_layer)
        .with(SqliteLayer {
            tx: db_tx.cloned(),
            min_level: db_min_level,
        });
    let _ = subscriber.try_init(); // 已初始化则忽略
}

/// 控制台 + 文本文件写入器(fmt 层 MakeWriter;与全局共用轮转句柄)。
struct IxrGoWriter;

impl<'a> tracing_subscriber::fmt::MakeWriter<'a> for IxrGoWriter {
    type Writer = IxrLineWriter;
    fn make_writer(&'a self) -> Self::Writer {
        IxrLineWriter
    }
}

/// ixr 日志行 writer:把 fmt 层产出的字节写到 控制台 + 文本文件。
struct IxrLineWriter;

impl Write for IxrLineWriter {
    fn write(&mut self, buf: &[u8]) -> std::io::Result<usize> {
        let line = String::from_utf8_lossy(buf);
        let _ = std::io::stdout().write_all(line.as_bytes()); // 控制台
        write_file_line(&line); // 文本文件(与全局共用轮转句柄)
        Ok(buf.len())
    }
    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

/// vendor 日志的 SQLite 接入层:捕获 (级别, 消息) 投递到 SQLite 写线程。
struct SqliteLayer {
    tx: Option<mpsc::Sender<DbMsg>>,
    min_level: Level,
}

impl<S> tracing_subscriber::Layer<S> for SqliteLayer
where
    S: tracing::Subscriber + for<'a> tracing_subscriber::registry::LookupSpan<'a>,
{
    fn on_event(&self, event: &tracing::Event<'_>, _ctx: tracing_subscriber::layer::Context<'_, S>) {
        let tx = match &self.tx {
            Some(tx) => tx,
            None => return, // 未启用入库
        };
        let level = level_from_tracing(*event.metadata().level());
        if level < self.min_level {
            return; // 低于入库最低级别:不入库
        }
        let mut visitor = MessageVisitor(String::new());
        event.record(&mut visitor);
        let _ = tx.send(DbMsg::Insert(LogRecord {
            time: chrono::Local::now().format("%Y-%m-%d %H:%M:%S%.3f").to_string(),
            level: level.as_str().to_string(),
            tags: String::new(),
            message: visitor.0,
            created_at: chrono::Utc::now().timestamp(),
        }));
    }
}

/// 事件字段访问器:收集 message 字段(其余字段忽略)。
struct MessageVisitor(String);

impl tracing::field::Visit for MessageVisitor {
    fn record_str(&mut self, field: &tracing::field::Field, value: &str) {
        if field.name() == "message" {
            self.0.push_str(value);
        }
    }
    fn record_debug(&mut self, field: &tracing::field::Field, value: &dyn fmt::Debug) {
        if field.name() == "message" {
            self.0.push_str(&format!("{value:?}"));
        }
    }
    fn record_i64(&mut self, _field: &tracing::field::Field, _value: i64) {}
    fn record_u64(&mut self, _field: &tracing::field::Field, _value: u64) {}
    fn record_bool(&mut self, _field: &tracing::field::Field, _value: bool) {}
    fn record_error(&mut self, _field: &tracing::field::Field, _value: &(dyn std::error::Error + 'static)) {}
}

// ============================================================================
// 单元测试
// ============================================================================

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::{Arc, Mutex};

    /// 格式化器:经 fmt 订阅器实际输出,断言 Go 风格行(含级别与附加字段)。
    #[test]
    fn go_style_formatter_output() {
        struct VecWriter(Arc<Mutex<Vec<u8>>>);
        impl Write for VecWriter {
            fn write(&mut self, buf: &[u8]) -> std::io::Result<usize> {
                self.0.lock().unwrap().extend_from_slice(buf);
                Ok(buf.len())
            }
            fn flush(&mut self) -> std::io::Result<()> {
                Ok(())
            }
        }
        struct Wrap(Arc<Mutex<Vec<u8>>>);
        impl<'a> tracing_subscriber::fmt::MakeWriter<'a> for Wrap {
            type Writer = VecWriter;
            fn make_writer(&'a self) -> Self::Writer {
                VecWriter(Arc::clone(&self.0))
            }
        }

        let out = Arc::new(Mutex::new(Vec::new()));
        let subscriber = tracing_subscriber::fmt()
            .event_format(GoStyleFormatter)
            .with_writer(Wrap(Arc::clone(&out)))
            .finish();
        tracing::subscriber::with_default(subscriber, || {
            tracing::info!("hello {}", 42);
            tracing::warn!(key = "val", "warn msg");
        });

        let s = String::from_utf8_lossy(&out.lock().unwrap()).into_owned();
        assert!(s.contains("[INFO] hello 42"), "缺 INFO 行:{s}");
        assert!(s.contains("[WARN] warn msg key=\"val\""), "缺 WARN 行(含附加字段):{s}");
    }

    #[test]
    fn level_from_tracing_mapping() {
        assert_eq!(level_from_tracing(tracing::Level::TRACE), Level::Debug);
        assert_eq!(level_from_tracing(tracing::Level::DEBUG), Level::Debug);
        assert_eq!(level_from_tracing(tracing::Level::INFO), Level::Info);
        assert_eq!(level_from_tracing(tracing::Level::WARN), Level::Warn);
        assert_eq!(level_from_tracing(tracing::Level::ERROR), Level::Error);
    }
}
