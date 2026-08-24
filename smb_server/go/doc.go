// Package smbgateway —— SMB 网关(Go 服务端)。
//
// ============================================================================
// 总体架构
// ============================================================================
//
//	Windows Explorer / Linux cifs
//	          │  445 (SMB 2.02/2.10/3.0/3.1.1)
//	          ▼
//	Rust SMB 网关(smb_server/rust,沿用 ixr-smb-server)
//	          │  出站私有 TCP 长连接(共享密钥鉴权 + 帧协议)
//	          ▼
//	Go 侧网关服务(smb_server/go,本包)—— 对接 OrbitCloud 后端
//	      ├─ auth.go          用户 / NT hash / ACL 查询与变更推送(动态认证)
//	      ├─ file_ops.go      文件操作主服务(open + 共享上下文解析)
//	      ├─ file_io.go       读 / 写 / 冲刷 / 元信息(stat/set_times/truncate)
//	      ├─ file_dir.go      列目录 / 关闭 / 删除 / 重命名 / 断连回收
//	      ├─ file_lookup.go   路径解析、记录查询与通用工具
//	      ├─ write_cache.go   写回缓存(解决 S3 不支持随机写)
//	      ├─ gateway.go       Socket 帧协议:连接管理、心跳、鉴权、请求池
//	      ├─ transport.go     帧收发与请求分发(读循环 → 协程池)
//	      ├─ handlers.go      OperateHandler 实现(操作码 → 业务服务转发)
//	      ├─ registry.go      远程句柄表与密码学工具
//	      ├─ protocol.go      帧协议常量、帧头结构与帧编解码
//	      ├─ types.go         总操作 JSON 与参数/结果类型
//	      ├─ shared_types.go  动态认证共享类型与哨兵错误
//	      ├─ operate.go       总操作路由(Route)与错误映射
//	      └─ wire.go          装配(由根 main.go 集成,与 HTTP 服务并行)
//
// 分层约定:
//
//	transport(帧收发) → dispatch(分发) → handlers(操作码转发)
//	          → auth / files(业务服务)→ core.Storage + server 层落库
//
// ============================================================================
// 控制面设计:总操作 JSON + 操作码路由(单次反序列化)
// ============================================================================
//
//	每个请求 body 只反序列化一次成 OperateRequest(总操作 JSON),
//	用操作码 Code 路由到具体操作(仿 HTTP 路由组):
//
//	    body = [4B jsonLen] + 总操作JSON(OperateRequest) + [流数据段(可选)]
//	                    │ 一次 json.Unmarshal
//	                    ▼
//	          OperateRequest.Route(handler) ── switch Code
//	                    │
//	                    ├─ 校验:Code ≠ 0 且对应指针非 nil(两者同时非 nil → 报错)
//	                    ├─ 调用 handler 对应方法(handler 由 Gateway 实现,
//	                    │    转发到 auth / files 业务服务)
//	                    └─ 返回 OperateResponse(总响应 JSON)+ 流数据段
//
//	约定:
//	  - Code 不能从 0 开始:0 = 未填写(校验层直接拒绝);
//	  - 操作参数(路径/意图/句柄/元数据)走 JSON;文件数据走流数据段
//	    (设计点 8:控制面 JSON、数据面 io.Reader 流式);
//	  - 流数据段仅 Read/Write 操作携带,其余操作 bodyLen == jsonLen。
//
//	帧消息类型因此精简为:
//	  MSG_OPERATE(请求)/ MSG_OPERATE_RESP(响应)承载全部控制面操作;
//	  MSG_AUTH_PUSH 保留为 Go→Rust 单向变更推送(无响应);
//	  握手/心跳仍为独立帧。
//
// ============================================================================
// 帧协议(与 smb_server/rust/src/types.rs 完全对应:字段语义/名称/顺序一致)
// ============================================================================
//
//	帧头 FrameHeader:固定 16 字节,大端序(big endian)
//	  offset  size  字段
//	  ──────  ────  ──────────────────────────────────────────────
//	  0       4     magic   = 0x4F435354("OCST")——快速丢弃非本协议连接
//	  4       1     version = 1
//	  5       1     flags   : 0x01=响应帧 | 0x02=需响应(请求) | 0x04=心跳
//	  6       2     msgType 消息类型(见 protocol.go 常量表)
//	  8       4     seq     请求序列号(响应原样带回;0=单向通知,无响应)
//	  12      4     bodyLen payload 字节数(≤ 16 MiB)
//	  ──────  ────  ──────────────────────────────────────────────
//
//	消息体 body:
//	  - MSG_OPERATE / MSG_OPERATE_RESP:
//	    [4B jsonLen u32] + 总操作/总响应 JSON + [流数据段(仅 Read/Write)];
//	  - 其余消息(握手/心跳/推送):JSON 或空。
//
//	错误:业务失败统一进 OperateResponse.Err(ErrorEnvelope,code 用 Err* 哨兵)。
//
// ============================================================================
// 连接模型(设计点 6/8)
// ============================================================================
//
//	Gateway(1) ── conn(每连接 1 协程:读循环)
//	                 │  读帧 → 入请求池(限并发+排队,池满回 ERR 背压)
//	                 │       → 协程处理(JSON 控制面 + io.Reader 流式数据面)
//	                 │       → 写响应
//	                 └  心跳由独立 goroutine 定期写 HEARTBEAT
//
//	请求背压:读循环只负责收帧,业务处理投协程池(core.AdmissionPool,
//	队列深度 = 配置 channel_buffer,与 Rust 侧管道缓冲对齐)。
package smbgateway
