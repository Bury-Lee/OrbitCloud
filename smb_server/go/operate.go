// operate.go —— 总操作路由:OperateHandler 接口 + Route 分发 + 错误映射。
//
// 路由层职责(设计点 8,控制面单次反序列化):
//   - 定义 OperateHandler 接口(由 Gateway 实现,见 handlers.go);
//   - OperateRequest.Route:校验 Code/指针唯一性 → 调 handler 对应方法
//     → 组装 OperateResponse(业务错误进 Err,不抛 Go error);
//   - wrapErrResp:handler 返回的 Go error → 哨兵错误码(ErrCode*)。
package smbgateway

import (
	"context" // 上下文(请求取消传播)
	"errors"  // errors.Is 错误链判断
	"orbitcloud/log"     // 服务日志(路由拒绝/错误映射记录)
	"reflect" // 反射判空(类型化 nil 指针)

	"gorm.io/gorm" // gorm.ErrRecordNotFound 映射

	"orbitcloud/server" // server 层业务错误(ErrNotFound 等)
)

// isFileOpCode 判定操作码是否文件类(需操作者身份,随操作帧携带 userId)。
func isFileOpCode(code OperateCode) bool {
	switch code {
	case CodeFileOpen, CodeFileRead, CodeFileWrite, CodeFileFlush, CodeFileStat,
		CodeFileSetTimes, CodeFileTruncate, CodeFileListDir, CodeFileClose,
		CodeFileUnlink, CodeFileRename:
		return true
	}
	return false // 认证类(QueryUser/QueryAcl/Snapshot)按自身 username 参数,不依赖身份
}

// codeName 操作码 → 可读名称(日志用)。
func codeName(code OperateCode) string {
	switch code { // 逐一映射
	case CodeAuthQueryUser:
		return "auth_query_user" // 查询用户
	case CodeAuthQueryAcl:
		return "auth_query_acl" // 查询可见共享
	case CodeAuthSnapshot:
		return "auth_snapshot" // 全量快照
	case CodeFileOpen:
		return "file_open" // 打开/创建
	case CodeFileRead:
		return "file_read" // 读
	case CodeFileWrite:
		return "file_write" // 写
	case CodeFileFlush:
		return "file_flush" // 冲刷
	case CodeFileStat:
		return "file_stat" // 元信息
	case CodeFileSetTimes:
		return "file_set_times" // 时间戳
	case CodeFileTruncate:
		return "file_truncate" // 截断
	case CodeFileListDir:
		return "file_list_dir" // 列目录
	case CodeFileClose:
		return "file_close" // 关闭
	case CodeFileUnlink:
		return "file_unlink" // 删除
	case CodeFileRename:
		return "file_rename" // 重命名
	default:
		return "unknown" // 未知码
	}
}

// OperateHandler 路由处理器(由 Gateway 实现,转发到 auth/files 业务服务)。
// 每个操作码对应一个方法,方法与操作参数/结果类型一一对应。
type OperateHandler interface {
	// HandleAuthQueryUser 查询用户凭据(CodeAuthQueryUser)。
	HandleAuthQueryUser(ctx context.Context, args *AuthUserArgs) (*AuthUserResult, error)
	// HandleAuthQueryAcl 查询用户可见共享清单(CodeAuthQueryAcl)。
	HandleAuthQueryAcl(ctx context.Context, args *AuthAclArgs) (*AuthAclResult, error)
	// HandleAuthSnapshot 全量同步快照(CodeAuthSnapshot)。
	HandleAuthSnapshot(ctx context.Context, args *SnapshotArgs) (*SnapshotResult, error)
	// HandleFileOpen 打开/创建(CodeFileOpen)。
	HandleFileOpen(ctx context.Context, args *OpenArgs) (*OpenResult, error)
	// HandleFileRead 按偏移读(CodeFileRead;返回流数据段,由 gateway 组装)。
	HandleFileRead(ctx context.Context, args *ReadArgs) (result *ReadResult, stream []byte, err error)
	// HandleFileWrite 按偏移写(CodeFileWrite;流数据段由 gateway 拆出)。
	HandleFileWrite(ctx context.Context, args *WriteArgs, stream []byte) (*WriteResult, error)
	// HandleFileFlush 冲刷写回缓存(CodeFileFlush)。
	HandleFileFlush(ctx context.Context, args *FlushArgs) error
	// HandleFileStat 查询元信息(CodeFileStat)。
	HandleFileStat(ctx context.Context, args *StatArgs) (*StatResult, error)
	// HandleFileSetTimes 设置时间戳(CodeFileSetTimes)。
	HandleFileSetTimes(ctx context.Context, args *SetTimesArgs) error
	// HandleFileTruncate 截断/扩展(CodeFileTruncate)。
	HandleFileTruncate(ctx context.Context, args *TruncateArgs) error
	// HandleFileListDir 列目录(CodeFileListDir)。
	HandleFileListDir(ctx context.Context, args *ListDirArgs) (*ListDirResult, error)
	// HandleFileClose 关闭句柄(CodeFileClose)。
	HandleFileClose(ctx context.Context, args *CloseArgs) error
	// HandleFileUnlink 删除路径(CodeFileUnlink)。
	HandleFileUnlink(ctx context.Context, args *UnlinkArgs) error
	// HandleFileRename 重命名/移动(CodeFileRename)。
	HandleFileRename(ctx context.Context, args *RenameArgs) error
}

// Route 路由并操作(仿 HTTP 路由组;在 gateway 收到 MSG_OPERATE 后调用,
// 整个 body 已在外部只反序列化过一次)。
//
// 参数:ctx 上下文;handler 路由处理器(Gateway 实现);stream 流数据段
// (仅 Write 请求携带,其余为 nil)。
// 返回值:resp 总响应 JSON(Code 回显,失败时 Err 非 nil);streamOut 流
// 数据段(仅 Read 响应携带);err 路由/校验级错误。
//
// 实现步骤:
//  1. Code 为 0 → ErrCodeBadRequest(视为根本没填写 JSON);
//  2. 已知性校验:未知操作码 → ErrCodeNotImpl;
//  3. 唯一性校验:Code 对应指针必须非 nil,且其余指针必须全 nil,
//     否则 → ErrCodeBadRequest(防误填,拒绝静默路由);
//  4. switch Code → 调 handler 对应方法;
//  5. 业务错误 → 填 resp.Err(哨兵 code),不抛 Go error;
//  6. 组装 resp{Code 回显, 结果指针} 返回。
func (r *OperateRequest) Route(ctx context.Context, handler OperateHandler, stream []byte) (resp *OperateResponse, streamOut []byte, err error) {
	// ---- 1. 哨兵校验:Code=0 视为根本没填写 JSON ----
	if r.Code == CodeInvalid { // 操作码为 0?
		log.Warnf("smb_gateway: 路由拒绝:code=0(JSON 未填充)") // 记录拒绝原因
		return errResp(ErrCodeBadRequest, "operate: code is 0 (json not filled)"), nil, nil // 回"请求不合法"
	}

	// ---- 2. 已知性校验:未知操作码直接 NotImpl ----
	switch r.Code {
	case CodeAuthQueryUser, CodeAuthQueryAcl, CodeAuthSnapshot, // 认证类操作码
		CodeFileOpen, CodeFileRead, CodeFileWrite, CodeFileFlush, // 文件类操作码
		CodeFileStat, CodeFileSetTimes, CodeFileTruncate, CodeFileListDir, // 元信息类
		CodeFileClose, CodeFileUnlink, CodeFileRename: // 生命周期类
		// 已知操作码,继续。
	default:
		log.Warnf("smb_gateway: 路由拒绝:未知操作码 %d", r.Code) // 记录拒绝原因
		return errResp(ErrCodeNotImpl, "operate: unknown code"), nil, nil // 未知操作码:回"未实现"
	}

	// ---- 3. 唯一性校验:Code 对应指针非 nil 且其余指针全 nil ----
	// 注意:类型化 nil 指针放进 interface{} 后 != nil,必须用 reflect 判空。
	codePtr := func(code OperateCode) any { // 闭包:操作码 → 对应参数指针
		switch code {
		case CodeAuthQueryUser:
			return r.AuthUser // 查询用户参数
		case CodeAuthQueryAcl:
			return r.AuthAcl // 查询 ACL 参数
		case CodeAuthSnapshot:
			return r.Snapshot // 快照参数
		case CodeFileOpen:
			return r.Open // 打开参数
		case CodeFileRead:
			return r.Read // 读参数
		case CodeFileWrite:
			return r.Write // 写参数
		case CodeFileFlush:
			return r.Flush // 冲刷参数
		case CodeFileStat:
			return r.Stat // 元信息参数
		case CodeFileSetTimes:
			return r.SetTimes // 时间戳参数
		case CodeFileTruncate:
			return r.Truncate // 截断参数
		case CodeFileListDir:
			return r.ListDir // 列目录参数
		case CodeFileClose:
			return r.Close // 关闭参数
		case CodeFileUnlink:
			return r.Unlink // 删除参数
		case CodeFileRename:
			return r.Rename // 重命名参数
		default:
			return nil // 已在上方已知性校验排除,不会到达
		}
	}
	// 记录每个字段的"预期归属"操作码,用于反查误填。
	fieldCodes := []struct { // 表:操作码 ↔ 指针(唯一性校验用)
		code OperateCode // 预期操作码
		ptr  any // 对应指针
	}{
		{CodeAuthQueryUser, r.AuthUser}, {CodeAuthQueryAcl, r.AuthAcl}, // 认证类字段
		{CodeAuthSnapshot, r.Snapshot}, {CodeFileOpen, r.Open}, // 快照/打开字段
		{CodeFileRead, r.Read}, {CodeFileWrite, r.Write}, // 读写字段
		{CodeFileFlush, r.Flush}, {CodeFileStat, r.Stat}, // 冲刷/元信息字段
		{CodeFileSetTimes, r.SetTimes}, {CodeFileTruncate, r.Truncate}, // 时间戳/截断字段
		{CodeFileListDir, r.ListDir}, {CodeFileClose, r.Close}, // 列目录/关闭字段
		{CodeFileUnlink, r.Unlink}, {CodeFileRename, r.Rename}, // 删除/重命名字段
	}
	// 本 Code 对应的指针必须非 nil。
	if isNilPtr(codePtr(r.Code)) { // 对应参数未填写?
		log.Warnf("smb_gateway: 路由拒绝:操作码 %d 参数未填充", r.Code) // 记录拒绝原因
		return errResp(ErrCodeBadRequest, "operate: args not filled for code"), nil, nil // 回"请求不合法"
	}
	// 其余指针必须全 nil(防误填)。
	for _, fc := range fieldCodes { // 遍历全部字段
		if fc.code != r.Code && !isNilPtr(fc.ptr) { // 非本操作码的字段被填了?
			log.Warnf("smb_gateway: 路由拒绝:操作码 %d 多个参数被填充(额外操作码 %d)", r.Code, fc.code) // 记录误填详情
			return errResp(ErrCodeBadRequest, "operate: multiple args filled"), nil, nil // 拒绝静默路由
		}
	}

	// ---- 4~6. switch 分发:调用 handler 对应方法并组装响应 ----
	switch r.Code {
	case CodeAuthQueryUser: // 查询用户凭据
		result, callErr := handler.HandleAuthQueryUser(ctx, r.AuthUser) // 调业务方法
		if callErr != nil { // 业务错误?
			return wrapErrResp(r.Code, callErr), nil, nil // 映射为哨兵错误响应
		}
		return &OperateResponse{Code: r.Code, AuthUser: result}, nil, nil // 组装总响应

	case CodeAuthQueryAcl: // 查询可见共享
		result, callErr := handler.HandleAuthQueryAcl(ctx, r.AuthAcl) // 调业务方法
		if callErr != nil { // 业务错误?
			return wrapErrResp(r.Code, callErr), nil, nil // 错误响应
		}
		return &OperateResponse{Code: r.Code, AuthAcl: result}, nil, nil // 成功响应

	case CodeAuthSnapshot: // 全量快照
		result, callErr := handler.HandleAuthSnapshot(ctx, r.Snapshot) // 调业务方法
		if callErr != nil { // 业务错误?
			return wrapErrResp(r.Code, callErr), nil, nil // 错误响应
		}
		return &OperateResponse{Code: r.Code, Snapshot: result}, nil, nil // 成功响应

	case CodeFileOpen: // 打开/创建
		result, callErr := handler.HandleFileOpen(ctx, r.Open) // 调业务方法
		if callErr != nil { // 业务错误?
			return wrapErrResp(r.Code, callErr), nil, nil // 错误响应
		}
		return &OperateResponse{Code: r.Code, Open: result}, nil, nil // 成功响应(句柄 ID)

	case CodeFileRead: // 按偏移读
		result, streamOut, callErr := handler.HandleFileRead(ctx, r.Read) // 调业务方法(返回流段)
		if callErr != nil { // 业务错误?
			return wrapErrResp(r.Code, callErr), nil, nil // 错误响应
		}
		_ = stream // Read 请求不携带流段
		return &OperateResponse{Code: r.Code, Read: result}, streamOut, nil // 成功:带流数据段

	case CodeFileWrite: // 按偏移写
		result, callErr := handler.HandleFileWrite(ctx, r.Write, stream) // 调业务方法(带流段数据)
		if callErr != nil { // 业务错误?
			return wrapErrResp(r.Code, callErr), nil, nil // 错误响应
		}
		return &OperateResponse{Code: r.Code, Write: result}, nil, nil // 成功响应(写入字节数)

	case CodeFileFlush: // 冲刷写回缓存
		if callErr := handler.HandleFileFlush(ctx, r.Flush); callErr != nil { // 调业务方法
			return wrapErrResp(r.Code, callErr), nil, nil // 错误响应
		}
		return &OperateResponse{Code: r.Code}, nil, nil // 无结果操作:仅 Code 回显

	case CodeFileStat: // 查询元信息
		result, callErr := handler.HandleFileStat(ctx, r.Stat) // 调业务方法
		if callErr != nil { // 业务错误?
			return wrapErrResp(r.Code, callErr), nil, nil // 错误响应
		}
		return &OperateResponse{Code: r.Code, Stat: result}, nil, nil // 成功响应(元信息)

	case CodeFileSetTimes: // 设置时间戳
		if callErr := handler.HandleFileSetTimes(ctx, r.SetTimes); callErr != nil { // 调业务方法
			return wrapErrResp(r.Code, callErr), nil, nil // 错误响应
		}
		return &OperateResponse{Code: r.Code}, nil, nil // 无结果操作

	case CodeFileTruncate: // 截断/扩展
		if callErr := handler.HandleFileTruncate(ctx, r.Truncate); callErr != nil { // 调业务方法
			return wrapErrResp(r.Code, callErr), nil, nil // 错误响应
		}
		return &OperateResponse{Code: r.Code}, nil, nil // 无结果操作

	case CodeFileListDir: // 列目录
		result, callErr := handler.HandleFileListDir(ctx, r.ListDir) // 调业务方法
		if callErr != nil { // 业务错误?
			return wrapErrResp(r.Code, callErr), nil, nil // 错误响应
		}
		return &OperateResponse{Code: r.Code, ListDir: result}, nil, nil // 成功响应(条目列表)

	case CodeFileClose: // 关闭句柄
		if callErr := handler.HandleFileClose(ctx, r.Close); callErr != nil { // 调业务方法
			return wrapErrResp(r.Code, callErr), nil, nil // 错误响应
		}
		return &OperateResponse{Code: r.Code}, nil, nil // 无结果操作

	case CodeFileUnlink: // 删除路径
		if callErr := handler.HandleFileUnlink(ctx, r.Unlink); callErr != nil { // 调业务方法
			return wrapErrResp(r.Code, callErr), nil, nil // 错误响应
		}
		return &OperateResponse{Code: r.Code}, nil, nil // 无结果操作

	case CodeFileRename: // 重命名/移动
		if callErr := handler.HandleFileRename(ctx, r.Rename); callErr != nil { // 调业务方法
			return wrapErrResp(r.Code, callErr), nil, nil // 错误响应
		}
		return &OperateResponse{Code: r.Code}, nil, nil // 无结果操作

	default: // 理论不可达(已知性校验已挡)
		return errResp(ErrCodeNotImpl, "operate: unknown code"), nil, nil // 兜底错误
	}
}

// errResp 构造仅含错误的总响应(Code=0,Err 填充)。
// 参数:code 哨兵错误码;msg 错误说明(仅日志)。
// 返回值:总响应结构。
func errResp(code uint32, msg string) *OperateResponse {
	return &OperateResponse{Err: &ErrorEnvelope{Code: code, Message: msg}} // 仅错误信封,无结果指针
}

// isNilPtr 判空(兼容类型化 nil 指针装入 interface{} 的情况)。
// 参数:v 任意接口值(可能为 nil 接口或 nil 指针)。
// 返回值:true 表示 nil(接口 nil 或指针 nil)。
// 背景:Go 中 (*T)(nil) 转 interface{} 后 != nil,必须经 reflect 判断。
func isNilPtr(v any) bool {
	if v == nil { // 接口本身为 nil?
		return true // 直接判空
	}
	rv := reflect.ValueOf(v) // 反射取底层值
	return rv.Kind() == reflect.Ptr && rv.IsNil() // 指针类型且为 nil 才算空
}

// wrapErrResp 把 handler 返回的 Go error 映射为哨兵错误响应。
// 参数:code 回显操作码;err 业务错误。
// 返回值:总响应结构(Code 回显,Err 按哨兵映射)。
// 映射表:errNotImplemented→NotImpl;server.ErrNotFound/gorm 未找到→NotFound;
// server.ErrForbidden→AccessDenied;server.ErrConflict→Exists;…其余→IO。
func wrapErrResp(code OperateCode, err error) *OperateResponse {
	msg := err.Error() // 取错误描述(仅日志)
	c := ErrCodeIO // 默认哨兵:IO 错误
	switch {
	case errors.Is(err, errNotImplemented): // 未实现功能
		c = ErrCodeNotImpl // 映射:未实现
	case errors.Is(err, server.ErrNotFound), errors.Is(err, gorm.ErrRecordNotFound): // 记录不存在
		c = ErrCodeNotFound // 映射:未找到
	case errors.Is(err, server.ErrForbidden): // 无权限
		c = ErrCodeAccessDenied // 映射:拒绝访问
	case errors.Is(err, server.ErrConflict): // 冲突(如重名)
		c = ErrCodeExists // 映射:已存在
	case errors.Is(err, errNotEmpty): // 目录非空
		c = ErrCodeNotEmpty // 映射:目录非空
	case errors.Is(err, errIsDirectory): // 目标是目录
		c = ErrCodeIsDirectory // 映射:是目录
	case errors.Is(err, errNotADirectory): // 目标不是目录
		c = ErrCodeNotADir // 映射:不是目录
	case errors.Is(err, server.ErrInvalidInput): // 输入非法
		c = ErrCodeBadRequest // 映射:请求不合法
	}
	log.Infof("smb_gateway: 操作=%s 错误码=%d(%s)", codeName(code), c, msg) // 记录错误映射(操作/哨兵码/描述)
	return &OperateResponse{Code: code, Err: &ErrorEnvelope{Code: c, Message: msg}} // 组装错误响应(Code 回显)
}
