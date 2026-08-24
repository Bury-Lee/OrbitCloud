//! ============================================================================
//! log/sqlite.rs —— 本地 SQLite 日志入库 + 定时清理
//! ============================================================================
//!
//! 设计:
//! - 独立写线程独占连接,串行处理 INSERT/清理消息,调用方仅投递(不阻塞);
//! - 无界通道:日志量大时仍不阻塞业务路径(写线程消费即可);
//! - 定时清理任务按 `retention_days` 删除过期记录(保留天数/周期可配置);
//! - 建表 DDL 与清理 SQL 集中于此,测试复用(见 CREATE_LOGS_TABLE)。

use std::sync::mpsc; // 写线程通道(非阻塞投递)

use crate::core::enter::LogConfig; // 日志配置

/// logs 表建表 DDL(含 created_at 索引;清理按该列删除)。
/// `pub(super)`:单元测试直接复用,保证与运行期建表一致。
pub(super) const CREATE_LOGS_TABLE: &str = "CREATE TABLE IF NOT EXISTS logs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    time       TEXT    NOT NULL,
    level      TEXT    NOT NULL,
    tags       TEXT    NOT NULL,
    message    TEXT    NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_logs_created_at ON logs(created_at);";

/// SQLite 线程消息(插入 / 清理)。
pub(super) enum DbMsg {
    /// 插入一条日志记录。
    Insert(LogRecord),
    /// 清理 created_at 早于 cutoff_ts 的过期记录。
    Cleanup { cutoff_ts: i64 },
}

/// 一条入库日志记录(与 Go 侧 model.Log 对齐:时间/级别/标记/内容)。
pub(super) struct LogRecord {
    /// 本地时间串(毫秒精度,与文本文件行一致)。
    pub(super) time: String,
    /// 级别(DEBUG/INFO/WARN/ERROR)。
    pub(super) level: String,
    /// 前缀标签(渲染后的 "[tag1] [tag2]",无标签为空串)。
    pub(super) tags: String,
    /// 消息内容。
    pub(super) message: String,
    /// 记录时刻(unix 秒,UTC;清理任务按此列删除)。
    pub(super) created_at: i64,
}

/// 启动 SQLite 写线程并建表(数据库文件路径见 db_path_of)。
///
/// 返回:写线程通道发送端;连接由线程独占(单写者,适配 SQLite)。
pub(super) fn sqlite_start(cfg: &LogConfig) -> Result<mpsc::Sender<DbMsg>, String> {
    let path = db_path_of(cfg);
    if let Some(parent) = std::path::Path::new(&path).parent() {
        if !parent.as_os_str().is_empty() {
            std::fs::create_dir_all(parent)
                .map_err(|e| format!("log: 创建 SQLite 目录 {} 失败:{e}", parent.display()))?;
        }
    }
    let conn = rusqlite::Connection::open(&path)
        .map_err(|e| format!("log: 打开 SQLite {} 失败:{e}", path))?;
    conn.execute_batch(CREATE_LOGS_TABLE)
        .map_err(|e| format!("log: 建表失败:{e}"))?;

    let (tx, rx) = mpsc::channel(); // 无界通道:日志投递不阻塞调用方
    std::thread::Builder::new()
        .name("log-sqlite".into())
        .spawn(move || sqlite_loop(conn, rx))
        .map_err(|e| format!("log: 启动 SQLite 线程失败:{e}"))?;
    Ok(tx)
}

/// SQLite 写线程主循环:串行处理插入/清理消息(通道关闭即退出)。
fn sqlite_loop(conn: rusqlite::Connection, rx: mpsc::Receiver<DbMsg>) {
    while let Ok(msg) = rx.recv() {
        match msg {
            DbMsg::Insert(rec) => {
                let res = conn.execute(
                    "INSERT INTO logs (time, level, tags, message, created_at) VALUES (?1, ?2, ?3, ?4, ?5)",
                    rusqlite::params![rec.time, rec.level, rec.tags, rec.message, rec.created_at],
                );
                if let Err(e) = res {
                    eprintln!("log: SQLite 插入失败:{e}"); // 本地补记(防静默丢日志)
                }
            }
            DbMsg::Cleanup { cutoff_ts } => {
                let res = conn.execute("DELETE FROM logs WHERE created_at < ?1", [cutoff_ts]);
                if let Err(e) = res {
                    eprintln!("log: SQLite 清理失败:{e}");
                }
            }
        }
    }
}

/// 定时清理任务:周期发送 Cleanup(删除保留天数之前的所有记录)。
pub(super) fn spawn_cleanup(tx: mpsc::Sender<DbMsg>, retention_days: u64, interval_secs: u64) {
    tokio::spawn(async move {
        let mut ticker = tokio::time::interval(std::time::Duration::from_secs(interval_secs));
        loop {
            ticker.tick().await;
            let cutoff = chrono::Utc::now().timestamp() - (retention_days as i64) * 86_400;
            if tx.send(DbMsg::Cleanup { cutoff_ts: cutoff }).is_err() {
                break; // 通道关闭(SQLite 线程退出)
            }
        }
    });
}

/// SQLite 数据库文件路径:配置 db_path 优先;空时 = {output}/logs.db
/// (output 也为空时退化为 ./logs/logs.db)。
fn db_path_of(cfg: &LogConfig) -> String {
    if !cfg.db_path.trim().is_empty() {
        return cfg.db_path.trim().to_string();
    }
    if !cfg.output.trim().is_empty() {
        return format!("{}/logs.db", cfg.output.trim());
    }
    "logs/logs.db".into()
}

// ============================================================================
// 单元测试
// ============================================================================

#[cfg(test)]
mod tests {
    use super::*;

    /// 全链路:建表 → 插入两条 → 按保留期限清理 → 断言剩余记录。
    #[test]
    fn sqlite_insert_then_cleanup() {
        let dir = std::env::temp_dir().join(format!("orbit-log-sqlite-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("test.db");

        let conn = rusqlite::Connection::open(&path).unwrap();
        conn.execute_batch(CREATE_LOGS_TABLE).unwrap();
        let (tx, rx) = mpsc::channel();
        let thread = std::thread::spawn(move || sqlite_loop(conn, rx));

        tx.send(DbMsg::Insert(LogRecord {
            time: "t1".into(),
            level: "INFO".into(),
            tags: "[网关]".into(),
            message: "hello".into(),
            created_at: 1_000,
        }))
        .unwrap();
        tx.send(DbMsg::Insert(LogRecord {
            time: "t2".into(),
            level: "WARN".into(),
            tags: String::new(),
            message: "stale".into(),
            created_at: 1,
        }))
        .unwrap();
        tx.send(DbMsg::Cleanup { cutoff_ts: 500 }).unwrap(); // 删除 created_at < 500
        drop(tx); // 关闭发送端 → 写线程退出
        thread.join().unwrap();

        let check = rusqlite::Connection::open(&path).unwrap();
        let count: i64 = check.query_row("SELECT COUNT(*) FROM logs", [], |r| r.get(0)).unwrap();
        assert_eq!(count, 1, "过期记录应被清理,仅剩 1 条");
        let msg: String = check
            .query_row("SELECT message FROM logs", [], |r| r.get(0))
            .unwrap();
        assert_eq!(msg, "hello");
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn db_path_precedence() {
        let cfg = |db_path: &str, output: &str| LogConfig {
            level: "info".into(),
            output: output.into(),
            file_prefix: "x".into(),
            db_enabled: true,
            db_path: db_path.into(),
            db_level: "info".into(),
            retention_days: 30,
            cleanup_interval_secs: 3600,
        };
        assert_eq!(db_path_of(&cfg("d/logs.db", "logs")), "d/logs.db");
        assert_eq!(db_path_of(&cfg("", "logs")), "logs/logs.db");
        assert_eq!(db_path_of(&cfg("", "")), "logs/logs.db");
    }
}
