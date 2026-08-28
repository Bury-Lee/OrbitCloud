// wire.go —— SMB 网关服务装配与接线(集成入口,非独立 main)。
//
// 本包是 OrbitCloud 主程序(smb_server/go 的上游调用方,见仓库根 main.go)
// 挂载的网关组件:主程序(根 main.go)统一启动双服务:
//
//	main.go(根,真实入口)
//	  ├─ 配置:flag.RunPreInit / appinit.InitConfig(缺失落盘内置默认)
//	  ├─ appinit: 日志 / DB(含 nt_hash 列迁移)/ JWT / 对象存储 / 协程池
//	  ├─ HTTP 服务(api.Router(),现有)
//	  ├─ SMB 网关服务(本包):
//	  │     auth = smbgateway.NewAuthService(db)
//	  │     files = smbgateway.NewFileOpsService(core.Storage, registry)
//	  │     pool = core.NewAdmissionPool(cfg.SmbGateway.MaxConcurrent,
//	  │             cfg.SmbGateway.ChannelBuffer, core.AdmissionModeReject)
//	  │     gw = smbgateway.NewGateway(cfg.SmbGateway.ListenAddr, key, pool, auth, files)
//	  │     go auth.WatchAndPush(ctx)          # 变更推送常驻
//	  │     go gw.Serve(ctx)                   # 私有 Socket,与 HTTP 并行
//	  └─ 优雅停机:两服务一起关闭
package smbgateway

import (
	"context"       // 上下文(停机传播)
	"encoding/json" // JSON 反序列化(-WithConfig)
	"errors"        // 错误构造(配置缺失)
	"fmt"           // 格式化错误           // 服务日志(装配记录)
	"os"            // 环境变量(共享密钥)

	"orbitcloud/config" // 配置类型(SmbGatewayConfig)
	"orbitcloud/core"   // 单例(DB/Storage)与准入池
	"orbitcloud/server" // 业务层(NtHashSyncHook 注入)
	"strings"           // 字符串处理(密钥去空白)
)

// BuildGateway 装配 SMB 网关服务(读密钥 → 构造请求池/auth/files/gateway)。
// 参数:cfg SMB 网关配置(config.yaml smb_gateway 分节)。
// 返回值:装配完成的网关(未开始监听;Serve 由调用方启动)。
// 错误:显式配置的共享密钥过短(配置直配或环境变量 <16 字节)→ 启动终止;
// 请求池构造失败 → 启动终止(与 HTTP 侧一致:存储/DB 不可用即终止)。
//
// 装配依赖图:
//
//	core.Storage(对象存储单例) ─┐
//	                              ├─► FileOpsService(文件操作)
//	HandleRegistry(句柄表) ──────┘
//	core.DB(数据库单例) ─► AuthService(认证 + 变更推送)
//	cfg(并发/队列) ─► AdmissionPool(请求准入池)
//	cfg.ListenAddr + 密钥 ─► Gateway ─► auth.SetPushFunc(gw.pushToAll)
func BuildGateway(cfg config.SmbGatewayConfig) (*Gateway, error) {
	// 1. 共享密钥:shared_key(配置直配)> shared_key_env(环境变量);两者皆空 =
	//    跳过静态鉴权(动态协商后续实现,见 resolveSharedKey)。
	key, err := resolveSharedKey(cfg) // 解析共享密钥(≥16 字节;皆空返回空切片)
	if err != nil {
		return nil, err // 显式配置的密钥过短:终止启动
	}

	// 2. 请求准入池(设计点 6:并发上限 + 队列深度,池满 reject 回背压帧)。
	pool := core.NewAdmissionPool(cfg.MaxConcurrent, cfg.ChannelBuffer, core.AdmissionModeReject) // 构造准入池

	// 3. 业务服务(auth 持 DB;files 持对象存储 + 句柄表)。
	auth := NewAuthService(core.DB) // 认证服务(绑定全局 DB)
	// 身份注册表(操作帧 userId → 用户信息,ristretto 缓存 60s 刷新)
	sr, err := NewSessionRegistry()
	if err != nil {
		return nil, fmt.Errorf("smb_gateway: init session registry: %w", err)
	}
	sessionRegistry = sr
	handles := NewHandleRegistry()                           // 句柄表
	files := NewFileOpsService(core.Storage, handles)        // 文件操作服务(全局存储 + 句柄表)

	// 注入用户注册/改密即时同步钩子:server 层改密后立即推送 NT hash,
	// SMB 旧密码立即失效(不等 60s 周期对账)。
	server.NtHashSyncHook = func(ctx context.Context, username, plainPassword string) {
		if err := auth.SetUserNTHash(ctx, username, plainPassword); err != nil {
			smbWarnf("smb_gateway: 同步用户 %q NT hash 失败:%v", username, err)
		}
	}

	// 4. 网关实例(认证服务与网关绑定:变更推送直连 pushToAll)。
	gw := NewGateway(cfg.ListenAddr, key, pool, auth, files) // 构造网关
	auth.SetPushFunc(gw.pushToAll) // 注入推送函数(DB 变更 → 广播)
	smbDebugf("smb_gateway: 网关已装配 监听=%s 池=%d/%d 静态密钥=%v", cfg.ListenAddr, cfg.MaxConcurrent, cfg.ChannelBuffer, len(key) > 0) // 记录装配结果(监听/并发/队列/是否静态密钥)
	return gw, nil // 返回装配完成的网关
}

// resolveSharedKey 解析共享密钥,优先级:shared_key(配置直配)> shared_key_env(环境变量)。
// 参数:cfg SMB 网关配置(shared_key / shared_key_env 字段)。
// 返回值:密钥字节(≥16);显式配置但过短 → 错误(启动即终止);
// 两者皆空 → 空切片 + 警告(跳过静态鉴权,动态密钥协商接口后续实现)。
func resolveSharedKey(cfg config.SmbGatewayConfig) ([]byte, error) {
	// 1. 配置直配优先(shared_key)。
	if raw := strings.TrimSpace(cfg.SharedKey); raw != "" { // 配置中显式写了密钥?
		if len(raw) < 16 { // 过短?
			return nil, fmt.Errorf("smb_gateway: 共享密钥过短(config shared_key %d 字节,需要 >= 16)", len(raw)) // 显式配置弱密钥:终止
		}
		return []byte(raw), nil // 配置直配生效
	}
	// 2. 回退:环境变量(shared_key_env;未定义/未设置均视为未配置)。
	if envName := strings.TrimSpace(cfg.SharedKeyEnv); envName != "" { // 配置了环境变量名?
		if raw := os.Getenv(envName); raw != "" { // 环境变量有值?
			if len(raw) < 16 { // 过短?
				return nil, fmt.Errorf("smb_gateway: 共享密钥过短(环境变量 %s %d 字节,需要 >= 16)", envName, len(raw)) // 显式提供的弱密钥:终止
			}
			return []byte(raw), nil // 环境变量生效
		}
	}
	// 3. 两者皆空:跳过静态鉴权(动态协商 TODO;空密钥下 HMAC 双侧一致,
	//    握手仍可通过,连接不受限)。
	smbWarnf("smb_gateway: 未配置共享密钥(shared_key 与 shared_key_env 均空),跳过静态鉴权(动态协商后续实现)") // 提示降级
	return nil, nil // 空密钥继续启动
}

// loadConfigFromJSON 从命令行 JSON 文本反序列化配置(设计点 7:-WithConfig)。
// 参数:raw JSON 文本(形如 {"smb_gateway":{"listen_addr":"…",…}})。
// 返回值:解析并校验通过的配置;非法 JSON/缺字段 → 错误(启动即终止)。
func loadConfigFromJSON(raw string) (config.SmbGatewayConfig, error) {
	cfg := config.DefaultSmbGatewayConfig() // 先取默认值(缺字段有兜底)
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil { // JSON 反序列化覆盖
		return config.SmbGatewayConfig{}, fmt.Errorf("smb_gateway: 解析 -WithConfig JSON 失败:%w", err) // 解析失败终止
	}
	if cfg.ListenAddr == "" || cfg.ChannelBuffer <= 0 || cfg.MaxConcurrent <= 0 { // 必填项校验
		return config.SmbGatewayConfig{}, errors.New("smb_gateway: -WithConfig JSON 缺少 listen_addr/channel_buffer/max_concurrent") // 缺项终止
	}
	return cfg, nil // 校验通过
}

// ServeWith 便捷启动:装配 + 监听(供独立进程/测试用;主程序走 BuildGateway + Serve)。
// 参数:ctx 上下文(取消即优雅停机);cfg SMB 网关配置。
// 返回值:serve 结束(停机)或装配/监听错误。
func ServeWith(ctx context.Context, cfg config.SmbGatewayConfig) error {
	gw, err := BuildGateway(cfg) // 装配
	if err != nil {
		return err // 装配失败上抛
	}
	return gw.Serve(ctx) // 启动监听(阻塞直到 ctx 取消)
}
