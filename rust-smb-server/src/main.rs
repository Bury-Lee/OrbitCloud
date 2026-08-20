use std::path::PathBuf;

use smb_server::{Access, Share, SmbServer};

mod winfs;

use winfs::WinFsBackend;

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| "info,smb_server=debug".into()),
        )
        .init();

    let root = std::env::var("SMB_ROOT")
        .map(PathBuf::from)
        .unwrap_or_else(|_| std::env::current_dir().expect("cwd").join("share"));
    std::fs::create_dir_all(&root)?;
    let hello = root.join("hello.txt");
    if !hello.exists() {
        std::fs::write(&hello, b"hello from rust smb 3.1.1 server\n")?;
    }

    let listen = std::env::var("SMB_LISTEN")
        .unwrap_or_else(|_| "0.0.0.0:2445".into())
        .parse()?;
    let user = std::env::var("SMB_USER").unwrap_or_else(|_| "alice".into());
    let pass = std::env::var("SMB_PASS").unwrap_or_else(|_| "password".into());

    tracing::info!(%listen, ?root, user = %user, "starting smb server (dialects 2.02 - 3.1.1)");

    let server = SmbServer::builder()
        .listen(listen)
        .user(&user, &pass)
        .share(Share::new("public", WinFsBackend::new(&root)?).public_read_only())
        .share(
            Share::new("home", WinFsBackend::new(&root)?)
                .user(&user, Access::ReadWrite),
        )
        .build()?;

    let addr = server.bind().await?;
    tracing::info!(%addr, "listening for smb connections");
    server.serve().await?;
    Ok(())
}
