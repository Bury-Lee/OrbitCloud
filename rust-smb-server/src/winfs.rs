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

pub struct WinFsBackend {
    root: PathBuf,
}

impl WinFsBackend {
    pub fn new(root: impl Into<PathBuf>) -> io::Result<Self> {
        let root = root.into();
        fs::create_dir_all(&root)?;
        Ok(Self { root })
    }

    fn host_path(&self, path: &SmbPath) -> PathBuf {
        let mut p = self.root.clone();
        for c in path.components() {
            p.push(c);
        }
        p
    }
}

#[async_trait]
impl ShareBackend for WinFsBackend {
    async fn open(&self, path: &SmbPath, opts: OpenOptions) -> SmbResult<Box<dyn Handle>> {
        let host = self.host_path(path);

        if opts.directory {
            const FILE_FLAG_BACKUP_SEMANTICS: u32 = 0x0200_0000;
            let meta = fs::metadata(&host);
            match meta {
                Ok(m) if m.is_file() => return Err(SmbError::NotADirectory),
                Ok(_) => {}
                Err(e) if e.kind() == io::ErrorKind::NotFound => {
                    if !matches!(opts.intent, OpenIntent::Create | OpenIntent::OpenOrCreate) {
                        return Err(SmbError::NotFound);
                    }
                    fs::create_dir(&host).map_err(map_io)?;
                }
                Err(e) => return Err(map_io(e)),
            }
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

        if opts.non_directory && fs::metadata(&host).map(|m| m.is_dir()).unwrap_or(false) {
            return Err(SmbError::IsDirectory);
        }

        let mut fo = FsOpenOptions::new();
        fo.read(opts.read).write(opts.write);
        match opts.intent {
            OpenIntent::Open => {
                if fs::metadata(&host).map(|m| m.is_dir()).unwrap_or(false) {
                    return Err(SmbError::IsDirectory);
                }
                fo.truncate(false).create(false);
            }
            OpenIntent::Create => {
                if fs::metadata(&host).is_ok() {
                    return Err(SmbError::Exists);
                }
                fo.truncate(false).create_new(true);
            }
            OpenIntent::OpenOrCreate => {
                fo.truncate(false).create(true);
            }
            OpenIntent::OverwriteOrCreate => {
                fo.truncate(true).create(true);
            }
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

    async fn unlink(&self, path: &SmbPath) -> SmbResult<()> {
        let host = self.host_path(path);
        match fs::remove_file(&host) {
            Ok(()) => Ok(()),
            Err(e) if e.kind() == io::ErrorKind::NotFound => Err(SmbError::NotFound),
            Err(e) if e.kind() == io::ErrorKind::PermissionDenied => {
                match fs::remove_dir(&host) {
                    Ok(()) => Ok(()),
                    Err(e2) if e2.kind() == io::ErrorKind::DirectoryNotEmpty => {
                        Err(SmbError::NotEmpty)
                    }
                    Err(e2) if e2.kind() == io::ErrorKind::NotFound => Err(SmbError::NotFound),
                    Err(e2) => Err(map_io(e2)),
                }
            }
            Err(e) => Err(map_io(e)),
        }
    }

    async fn rename(&self, from: &SmbPath, to: &SmbPath) -> SmbResult<()> {
        let host_from = self.host_path(from);
        let host_to = self.host_path(to);
        if fs::metadata(&host_to).is_ok() {
            return Err(SmbError::Exists);
        }
        fs::rename(&host_from, &host_to).map_err(map_io)
    }

    fn capabilities(&self) -> BackendCapabilities {
        BackendCapabilities {
            is_read_only: false,
            case_sensitive: false,
        }
    }
}

pub struct WinHandle {
    file: Mutex<fs::File>,
    path: PathBuf,
    is_dir: bool,
    delete_on_close: bool,
}

fn map_io(e: io::Error) -> SmbError {
    match e.kind() {
        io::ErrorKind::NotFound => SmbError::NotFound,
        io::ErrorKind::PermissionDenied => SmbError::AccessDenied,
        io::ErrorKind::AlreadyExists => SmbError::Exists,
        io::ErrorKind::DirectoryNotEmpty => SmbError::NotEmpty,
        io::ErrorKind::InvalidInput => SmbError::IsDirectory,
        _ => SmbError::Io(e),
    }
}

fn file_info(name: String, meta: &fs::Metadata) -> FileInfo {
    FileInfo {
        name,
        end_of_file: meta.len(),
        allocation_size: meta.len(),
        creation_time: meta.creation_time(),
        last_access_time: meta.last_access_time(),
        last_write_time: meta.last_write_time(),
        change_time: meta.last_write_time(),
        is_directory: meta.is_dir(),
        file_index: 0,
    }
}

fn filetime_to_system_time(ft: u64) -> SystemTime {
    const EPOCH_DIFF_TICKS: u64 = 116_444_736_000_000_000;
    let nanos = ft.saturating_sub(EPOCH_DIFF_TICKS) * 100;
    UNIX_EPOCH + Duration::from_nanos(nanos)
}

#[async_trait]
impl Handle for WinHandle {
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
        buf.truncate(n);
        Ok(Bytes::from(buf))
    }

    async fn write(&self, offset: u64, data: &[u8]) -> SmbResult<u32> {
        if self.is_dir {
            return Err(SmbError::IsDirectory);
        }
        let n = self
            .file
            .lock()
            .map_err(|_| SmbError::Io(io::Error::other("lock poisoned")))?
            .seek_write(data, offset)
            .map_err(map_io)?;
        Ok(n as u32)
    }

    async fn flush(&self) -> SmbResult<()> {
        self.file
            .lock()
            .map_err(|_| SmbError::Io(io::Error::other("lock poisoned")))?
            .flush()
            .map_err(map_io)
    }

    async fn stat(&self) -> SmbResult<FileInfo> {
        let meta = fs::metadata(&self.path).map_err(map_io)?;
        let name = self
            .path
            .file_name()
            .map(|n| n.to_string_lossy().into_owned())
            .unwrap_or_else(|| self.path.to_string_lossy().into_owned());
        Ok(file_info(name, &meta))
    }

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

    async fn close(self: Box<Self>) -> SmbResult<()> {
        if let Ok(mut f) = self.file.lock() {
            let _ = f.flush();
        }
        if self.delete_on_close {
            let p = self.path.clone();
            let _ = fs::remove_file(&p);
            let _ = fs::remove_dir(&p);
        }
        Ok(())
    }
}
