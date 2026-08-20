//! ============================================================================
//! main.rs — Rust SMB 3.1.1 服务器入口
//! ============================================================================
//!
//! 功能:在 Windows 上启动一个 SMB 服务器,监听指定端口(默认 2445),
//! 对外提供两个共享:
//!   - `public` —— 匿名只读
//!   - `home`   —— 用户读写(默认用户 alice / password)
//!
//! 底层使用 `ixr-smb-server` 库(SMB 2.02 / 2.10 / 3.0 / 3.0.2 / 3.1.1
//! 方言协商,支持 SMB 3.1.1 的 preauth integrity、协商上下文、签名等),
//! 存储层使用本工程自研的 Windows 文件系统后端(见 winfs.rs)。
//!
//! 所有配置均可通过环境变量覆盖:
//!   SMB_LISTEN  监听地址          (默认 0.0.0.0:2445)
//!   SMB_ROOT    共享根目录        (默认 <当前目录>/share)
//!   SMB_USER    读写用户名        (默认 alice)
//!   SMB_PASS    用户密码          (默认 password)
//!   RUST_LOG    日志级别          (默认 info,smb_server=debug)
//!
//! 日志:启动后可用 `Get-Content server.log -Tail 50` 查看。

use std::path::PathBuf;

use smb_server::{Access, Share, SmbServer};

// 引入自研的 Windows 文件系统后端(实现库的 ShareBackend trait)
mod winfs;

use winfs::WinFsBackend;

/// 程序入口(tokio 异步运行时)。
///
/// 启动流程:
///   1. 初始化 tracing 日志
///   2. 确定共享根目录并创建(首次运行写入 hello.txt 用于快速验证)
///   3. 从环境变量读取监听地址与账号
///   4. 构建 SMB 服务器(两个共享:public 匿名只读 / home 用户读写)
///   5. bind(绑定端口)→ serve(进入 accept 循环,阻塞直到退出)
#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    // ---- 1. 日志初始化 ----
    // 默认级别 info,并对 smb_server 模块开 debug(可看到协商/建树等细节);
    // 可用环境变量 RUST_LOG 覆盖。
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| "info,smb_server=debug".into()),
        )
        .init();

    // ---- 2. 共享根目录 ----
    // 优先取 SMB_ROOT,否则用当前工作目录下的 share/。
    let root = std::env::var("SMB_ROOT")
        .map(PathBuf::from)
        .unwrap_or_else(|_| std::env::current_dir().expect("cwd").join("share"));
    // 根目录不存在则创建(注意:必须在写 hello.txt 之前创建,
    // 否则 fs::write 会因父目录缺失报"系统找不到指定的路径")。
    std::fs::create_dir_all(&root)?;
    // 写一个欢迎文件,方便客户端 ls 时立即确认可读。
    let hello = root.join("hello.txt");
    if !hello.exists() {
        std::fs::write(&hello, b"hello from rust smb 3.1.1 server\n")?;
    }

    // ---- 3. 监听地址与凭据(环境变量覆盖,带默认值) ----
    let listen = std::env::var("SMB_LISTEN")
        .unwrap_or_else(|_| "0.0.0.0:2445".into())
        .parse()?;
    let user = std::env::var("SMB_USER").unwrap_or_else(|_| "alice".into());
    let pass = std::env::var("SMB_PASS").unwrap_or_else(|_| "password".into());

    tracing::info!(%listen, ?root, user = %user, "starting smb server (dialects 2.02 - 3.1.1)");

    // ---- 4. 构建 SMB 服务器 ----
    // 注意:Share 不允许同时配置用户权限与 public 模式(库校验
    // PublicMixedWithUsers),所以拆分两个共享:
    //   - public:匿名只读,方便不输账号快速验证连通性
    //   - home:  用户 alice 读写(读写验证用这个)
    let server = SmbServer::builder()
        .listen(listen)
        .user(&user, &pass)
        .share(Share::new("public", WinFsBackend::new(&root)?).public_read_only())
        .share(
            Share::new("home", WinFsBackend::new(&root)?)
                .user(&user, Access::ReadWrite),
        )
        .build()?;

    // ---- 5. 绑定端口并进入服务循环 ----
    // bind() 返回实际绑定的地址;serve() 开始 accept,直到进程退出。
    let addr = server.bind().await?;
    tracing::info!(%addr, "listening for smb connections");
    server.serve().await?;
    Ok(())
}
