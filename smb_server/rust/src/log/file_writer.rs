//! ============================================================================
//! log/file_writer.rs —— 文本日志文件 writer(与 Go 侧 rotate.go 对齐)
//! ============================================================================
//!
//! 目录结构:`{output}/YYYY-MM-DD/{file_prefix}-HH-MM-SS.log`:
//! - 每次启动:以启动时刻(HH-MM-SS)命名当日文件;
//! - 跨天(日期变化):自动切换到新日期子文件夹,开启新文件;
//! - 同一秒内重复轮转(或同秒重启):追加 .1/.2 序号,避免混写。
//!
//! 并发安全:由调用方(mod.rs 的 Mutex<Option<DailyFile>>)串行化访问。

use std::io::Write; // 文件写入原语(write_all)

/// 每日轮转文本文件 writer。
pub(super) struct DailyFile {
    /// 日志根目录(日期子文件夹的父目录)。
    dir: std::path::PathBuf,
    /// 文件名前缀(不含扩展名)。
    prefix: String,
    /// 当前文件所属日期(YYYY-MM-DD;写前检查是否跨天)。
    day: String,
    /// 当前文件句柄(None = 未打开)。
    file: Option<std::fs::File>,
}

impl DailyFile {
    /// 打开今日日志文件(创建日期子文件夹与启动时刻命名的文件)。
    pub(super) fn open(dir: &str, prefix: &str) -> Result<Self, String> {
        let now = chrono::Local::now();
        let mut daily = DailyFile {
            dir: std::path::PathBuf::from(dir),
            prefix: if prefix.trim().is_empty() {
                "smb-gateway".into()
            } else {
                prefix.trim().into()
            },
            day: now.format("%Y-%m-%d").to_string(),
            file: None,
        };
        daily.rotate(now).map_err(|e| format!("log: 打开日志文件失败:{e}"))?;
        Ok(daily)
    }

    /// 写一行(跨天自动轮转;失败仅丢弃)。
    pub(super) fn write(&mut self, bytes: &[u8]) -> std::io::Result<()> {
        let now = chrono::Local::now();
        if now.format("%Y-%m-%d").to_string() != self.day {
            self.rotate(now)?; // 跨天:切换日期子文件夹并开新文件
        }
        if let Some(f) = self.file.as_mut() {
            f.write_all(bytes)?;
        }
        Ok(())
    }

    /// 关闭旧文件并打开新文件(按 now 的日期/时分秒命名)。
    fn rotate(&mut self, now: chrono::DateTime<chrono::Local>) -> std::io::Result<()> {
        self.file.take(); // 关闭旧文件(句柄 drop 即关闭)

        let day = now.format("%Y-%m-%d").to_string();
        let day_dir = self.dir.join(&day);
        std::fs::create_dir_all(&day_dir)?; // 建日期子文件夹(幂等)

        let stamp = now.format("%H-%M-%S").to_string();
        let mut path = day_dir.join(format!("{}-{}.log", self.prefix, stamp));
        let mut i = 1;
        while path.exists() {
            // 同秒冲突:追加 .1/.2 序号
            path = day_dir.join(format!("{}-{}.{}.log", self.prefix, stamp, i));
            i += 1;
        }
        let f = std::fs::OpenOptions::new()
            .create(true)
            .append(true)
            .write(true)
            .open(&path)?;
        self.day = day;
        self.file = Some(f);
        Ok(())
    }
}

// ============================================================================
// 单元测试
// ============================================================================

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn daily_file_rotation() {
        let dir = std::env::temp_dir().join(format!("orbit-log-test-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        let mut daily = DailyFile::open(dir.to_str().unwrap(), "test-prefix").expect("open");
        let _ = daily.write(b"line1\n");
        let day_dir = dir.join(chrono::Local::now().format("%Y-%m-%d").to_string());
        assert!(day_dir.exists(), "日期子文件夹应已创建");
        let mut files: Vec<_> = std::fs::read_dir(&day_dir)
            .unwrap()
            .map(|e| e.unwrap().file_name().to_string_lossy().into_owned())
            .collect();
        files.sort();
        assert_eq!(files.len(), 1, "应只有一个日志文件:{files:?}");
        assert!(files[0].starts_with("test-prefix-"), "文件名前缀:{}", files[0]);
        assert!(files[0].ends_with(".log"), "日志扩展名:{}", files[0]);
        let data = std::fs::read(day_dir.join(&files[0])).unwrap();
        assert_eq!(String::from_utf8_lossy(&data), "line1\n");
        let _ = std::fs::remove_dir_all(&dir);
    }
}
