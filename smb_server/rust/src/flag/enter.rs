//! ============================================================================
//! flag/enter.rs —— 配置读取与解析
//! ============================================================================
//!
//! - 内置默认:`include_str!("../../config.yaml")`(编译期嵌入);
//! - 读取解析:优先工作目录 ./config.yaml,缺失落盘内置默认后解析;
//! - 解析库:serde_yaml(与 Go 侧 gopkg.in/yaml.v3 语义对齐)。

use std::env; // 环境变量读取(共享密钥注入)
use std::fs; // 文件读写(配置读取/落盘)
use std::net::SocketAddr; // 监听/网关地址校验

use crate::core::enter::Config; // 全局配置结构

/// 内置默认配置(编译期嵌入;与包根 config.yaml 保持同步,禁止手工漂移)。
///
/// 意义:首次启动无配置文件时,把此内容落盘为 ./config.yaml,
/// 与 Go 侧"内置 embed 默认配置"行为一致。
pub const DEFAULT_CONFIG: &str = include_str!("../../config.yaml"); // 编译期把 config.yaml 内容嵌进二进制

/// 默认配置文件路径(工作目录相对;与 Go 侧 `./config.yaml` 约定一致)。
pub const DEFAULT_CONFIG_PATH: &str = "./config.yaml";

/// 应用版本号(与 Go 侧 --version 输出对齐)。
pub const VERSION: &str = "0.1.0";

/// 加载并解析配置(入口)。
///
/// 参数:
/// - `path`:配置文件路径(默认传 DEFAULT_CONFIG_PATH)。
///
/// 返回值:
/// - Ok(Config):解析成功后的全局配置;
/// - Err(String):配置缺失/非法/校验失败(调用方启动即终止,不做兜底)。
pub fn load_config(path: &str) -> Result<Config, String> {
    if !std::path::Path::new(path).exists() { // 配置文件不存在?(首次启动)
        fs::write(path, DEFAULT_CONFIG) // 用编译期内置默认落盘(让用户拿到可改的样板)
            .map_err(|e| format!("配置:写入默认配置 {} 失败: {}", path, e))?; // 写盘失败终止
        infof!("配置:默认配置已写入 {}", &["配置"], path); // 记录自动生成
    }
    let data = fs::read_to_string(path) // 读取配置文件全部内容
        .map_err(|e| format!("配置:读取 {} 失败: {}", path, e))?; // 读失败终止
    let cfg: Config = serde_yaml::from_str(&data) // YAML → Config(缺失字段走 serde 默认值)
        .map_err(|e| format!("配置:解析 {} 失败: {}", path, e))?; // 解析失败终止(带文件名)
    validate(&cfg)?; // 语义校验(必填项/地址可解析/白名单),失败即终止启动
    debugf!(
        "配置:已加载 监听={} 网关={} 缓冲={} 对账间隔={}s 日志级别={}",
        &["配置"], // 前缀标签
        cfg.smb.listen, // SMB 监听地址
        cfg.gateway.addr, // 网关地址
        cfg.gateway.channel_buffer, // 管道缓冲
        cfg.gateway.sync_interval_secs, // 对账周期
        cfg.log.level // 日志级别
    ); // 记录配置摘要(不含密钥)
    Ok(cfg)
}

/// 必填项与合法性校验(反序列化后的二次检查)。
///
/// 返回值:
/// - Ok(()):校验通过;
/// - Err(String):缺失/非法项说明(逐项列出,便于定位)。
pub fn validate(config: &Config) -> Result<(), String> {
    let mut problems: Vec<String> = Vec::new(); // 问题收集器(全部查完一次性报出)
    if config.smb.listen.trim().is_empty() { // SMB 监听地址为空?
        problems.push("smb.listen 为必填项".into()); // 记必填缺失
    } else if config.smb.listen.parse::<SocketAddr>().is_err() { // 非空但解析失败?
        problems.push(format!("smb.listen {:?} 不是合法的地址:端口", config.smb.listen)); // 记格式错误
    }
    if config.gateway.addr.trim().is_empty() { // 网关地址为空?
        problems.push("gateway.addr 为必填项".into()); // 记必填缺失
    } else if config.gateway.addr.parse::<SocketAddr>().is_err() { // 非空但解析失败?
        problems.push(format!("gateway.addr {:?} 不是合法的地址:端口", config.gateway.addr)); // 记格式错误
    }
    if config.gateway.channel_buffer == 0 { // 管道缓冲为 0?(会退化为无缓冲,失去背压意义)
        problems.push("gateway.channel_buffer 必须大于 0".into()); // 记非法值
    }
    match config.log.level.as_str() { // 日志级别白名单校验
        "debug" | "info" | "warn" | "error" => {} // 合法级别:通过
        other => problems.push(format!( // 非法级别:记问题
            "log.level {:?} 非法(debug|info|warn|error)",
            other
        )),
    }
    if problems.is_empty() { // 没有任何问题?
        Ok(()) // 校验通过
    } else {
        Err(format!("配置:非法: {}", problems.join("; "))) // 拼接全部问题一次性报错
    }
}

/// 读取共享密钥,优先级:shared_key(配置直配)> shared_key_env(环境变量)。
///
/// 参数:
/// - `cfg_shared_key`:配置中直配的密钥(shared_key;空 = 未配置);
/// - `env_name`:环境变量名(shared_key_env;空 = 未配置)。
///
/// 返回值:
/// - Ok(Vec<u8>):密钥字节(长度 ≥ 16);
/// - Err(String):显式配置(直配或环境变量)但过短 —— 启动即终止;
/// - Ok(Vec::new()):两者皆空 —— 跳过静态鉴权(动态协商后续实现)。
pub fn load_shared_key(cfg_shared_key: &str, env_name: &str) -> Result<Vec<u8>, String> {
    // 1. 配置直配优先(shared_key)。
    let raw = if !cfg_shared_key.trim().is_empty() { // 配置中显式写了密钥?
        cfg_shared_key.trim().to_string() // 直配生效
    } else if !env_name.trim().is_empty() { // 回退:环境变量名已配置?
        env::var(env_name).unwrap_or_default() // 环境变量有值则取用;未定义视为未配置
    } else {
        String::new() // 两者皆空
    };
    if raw.is_empty() { // 均未配置?
        warnf!("配置:未配置共享密钥(shared_key 与 shared_key_env 均空),跳过静态鉴权(动态协商后续实现)", &["配置"]); // 提示降级
        return Ok(Vec::new()); // 空密钥继续启动(空密钥下 HMAC 双侧一致,握手可通过)
    }
    if raw.len() < 16 { // 过短?
        return Err(format!( // 显式配置的弱密钥:硬性拒绝
            "配置:共享密钥过短({} 字节,需要 >= 16)",
            raw.len()
        ));
    }
    Ok(raw.into_bytes()) // 字符串 → 字节(握手 HMAC 用)
}

/// 命令行入口(仿 Go 侧 flag 包:-initConfig 等指令)。
///
/// 参数:
/// - `args`:命令行参数(首元素为程序名)。
///
/// 返回值:
/// - Ok(Some(Config)):需要继续启动;
/// - Ok(None):指令已处理,正常退出(如 --version / -initConfig 落盘后退出);
/// - Err(String):非法参数/指令失败。
pub fn parse_args(args: &[String]) -> Result<Option<Config>, String> {
    if args.len() <= 1 { // 无参数(只有程序名)?
        return Ok(Some(load_config(DEFAULT_CONFIG_PATH)?)); // 默认启动路径:加载 ./config.yaml
    }
    let cmd = args[1].as_str(); // 取第二个参数作为指令
    match cmd {
        "-initConfig" | "--initConfig" => { // 生成配置样板指令
            fs::write(DEFAULT_CONFIG_PATH, DEFAULT_CONFIG) // 用内置默认强制覆盖落盘
                .map_err(|e| format!("指令:写入默认配置失败: {}", e))?; // 写盘失败终止
            println!("默认配置已写入 {}", DEFAULT_CONFIG_PATH); // 提示用户
            Ok(None) // 指令已处理,退出
        }
        "--version" | "-version" => { // 版本指令
            println!("smb-gateway {}", VERSION); // 打印版本号
            Ok(None) // 指令已处理,退出
        }
        "--help" | "-h" | "-help" => { // 帮助指令
            println!("smb-gateway (OrbitCloud SMB 网关 Rust 侧)"); // 程序简介
            println!("用法:"); // 用法头
            println!("  smb-gateway [flags]"); // 语法行
            println!("    (无参数)          正常启动(读取 ./config.yaml,缺失自动落盘默认配置)"); // 默认行为
            println!("  smb-gateway -initConfig   用内置默认配置覆盖生成 ./config.yaml 后退出"); // 指令 1
            println!("  smb-gateway --version     打印版本号后退出"); // 指令 2
            println!("  smb-gateway --help / -h   打印帮助后退出"); // 指令 3
            println!("  smb-gateway --WithConfig <json>  用 JSON 注入配置启动(跳过 config.yaml)"); // 指令 4
            println!("共享密钥:经环境变量 {} 注入(≥16 字节;留空 = 跳过静态密钥)", // 密钥说明
                crate::core::enter::DEFAULT_KEY_ENV);
            Ok(None) // 指令已处理,退出
        }
        "--WithConfig" | "-WithConfig" => { // JSON 注入配置指令(容器/编排场景,JSON 兼容 YAML 子集)
            if args.len() < 3 { // 缺少 JSON 参数?
                return Err("指令:--WithConfig 需要 JSON 参数".into()); // 报错提示
            }
            let cfg: Config = serde_yaml::from_str(args[2].as_str()) // 解析注入的 JSON 为 Config
                .map_err(|e| format!("指令:解析 --WithConfig JSON 失败: {}", e))?; // 解析失败终止
            validate(&cfg)?; // 同样的语义校验
            Ok(Some(cfg)) // 用注入配置继续启动(跳过 config.yaml)
        }
        other => Err(format!("指令:未知参数 {:?}(试试 --help)", other)), // 未知参数:报错不静默(避免配置失效不自知)
    }
}
