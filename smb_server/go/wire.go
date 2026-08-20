// wire.go —— SMB 网关服务装配与接线(集成入口,非独立 main)。
//
// 本包是 OrbitCloud 主程序(smb_server/go 的上游调用方,见仓库根 main.go)
// 挂载的网关组件:**不提供 func main()**,主程序仍在根 main.go——
// 双服务(设计点 8)都由主程序统一启动:
//
//	main.go(根,真实入口)
//	  ├─ 配置:flag.RunPreInit / appinit.InitConfig(缺失落盘内置默认)
//	  │        (+ 设计点 7:命令行 -WithConfig <json> 注入可跳过 config.yaml)
//	  ├─ appinit: 日志 / DB(含 nt_hash 列迁移)/ JWT / 对象存储 / 协程池
//	  ├─ HTTP 服务(api.Router(),现有)
//	  ├─ SMB 网关服务(本包,伪代码接线):
//	  │     auth = smbgateway.NewAuthService(db)
//	  │     files = smbgateway.NewFileOpsService(core.Storage, registry)
//	  │     pool = core.NewAdmissionPool(cfg.SmbGateway.MaxConcurrent,
//	  │             cfg.SmbGateway.ChannelBuffer, core.AdmissionModeReject)
//	  │     gw = smbgateway.NewGateway(cfg.SmbGateway.ListenAddr, key, pool, auth, files)
//	  │     go auth.WatchAndPush(ctx)          # 变更推送常驻
//	  │     go gw.Serve(ctx)                   # 私有 Socket,与 HTTP 并行
//	  └─ 优雅停机:两服务一起关闭
//
// 依赖注入(伪代码阶段约定):配置段与 Rust 侧 config.yaml `gateway:` 分节对齐:
//
//	smb_gateway:
//	  listen_addr: 127.0.0.1:9001            # 私有 Socket,仅 Rust 网关可访问
//	  shared_key_env: ORBITCLOUD_SMB_GATEWAY_KEY  # 密钥走环境变量注入
//	  channel_buffer: 1024                   # 请求管道缓冲(与 Rust 侧对齐)
//	  max_concurrent: 64                     # 协程池同时在途上限
package smbgateway

import (
	"encoding/json"
	"errors"
)

// SMBGatewayConfig SMB 网关服务配置(与 Rust 侧 config.yaml `gateway:` 分节
// 字段一一对应;真实现并入 OrbitCloud config 包)。
type SMBGatewayConfig struct {
	// ListenAddr 私有 TCP 监听地址(仅 Rust 网关可访问)。
	ListenAddr string
	// SharedKeyEnv 共享密钥所在环境变量名(密钥禁止写入配置文件;
	// 留空 = 握手后双方动态协商随机密钥,接口后续实现)。
	SharedKeyEnv string
	// ChannelBuffer 请求管道缓冲长度(设计点 6:与 Rust 侧 channel_buffer 对齐,
	// 即请求池排队深度)。
	ChannelBuffer int
	// MaxConcurrent 请求池同时在途上限(设计点 6:Go 侧协程池并发数)。
	MaxConcurrent int
}

// DefaultSMBGatewayConfig 网关配置缺省值(配置未显式给出时兜底;与 Rust 侧
// config.yaml 内置默认保持一致)。
func DefaultSMBGatewayConfig() SMBGatewayConfig {
	return SMBGatewayConfig{
		ListenAddr:    "127.0.0.1:9001",
		SharedKeyEnv:  "ORBITCLOUD_SMB_GATEWAY_KEY",
		ChannelBuffer: 1024,
		MaxConcurrent: 64,
	}
}

// loadConfigFromJSON 从命令行 JSON 文本反序列化配置(设计点 7:-WithConfig)。
// 参数:raw JSON 文本(形如 {"smb_gateway":{"listen_addr":"…",…}})。
// 返回值:解析并校验通过的配置;非法 JSON/缺字段 → 错误(启动即终止)。
// 伪代码步骤:
//
//	1. json.Unmarshal(raw) 到配置结构;语法错误 → Err(带位置);
//	2. 与 DefaultSMBGatewayConfig 合并(未给出的字段用缺省值);
//	3. 校验必填项(shared_key_env 可留空;channel_buffer > 0;max_concurrent > 0);
//	4. 返回配置。
func loadConfigFromJSON(raw string) (SMBGatewayConfig, error) {
	_ = raw
	return SMBGatewayConfig{}, errNotImplemented
}

// loadSharedKey 读取共享密钥(环境变量注入,禁止写入 config.yaml)。
// 参数:envName 环境变量名(如 ORBITCLOUD_SMB_GATEWAY_KEY;留空 = 跳过静态
// 密钥,握手后动态协商)。
// 返回值:密钥字节;未设置返回错误(启动即终止,不做缺省兜底)。
// 伪代码步骤:os.Getenv → 长度 ≥ 16 校验 → 返回。
func loadSharedKey(envName string) ([]byte, error) {
	_ = envName
	return nil, errors.New("smb_gateway: shared key not set")
}

// 编译期断言:本文件依赖的 JSON(伪代码占位)。
var _ = json.Marshal
