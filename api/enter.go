// Package api 提供 HTTP 接入层:路由注册、鉴权、参数校验、权限预检与错误响应映射。
// 本包不包含业务逻辑,业务由 server 包提供;路由按模块拆分在 router_*.go 中注册。
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"orbitcloud/common"
	"orbitcloud/core"
)

// claimsKey 鉴权 Claims 在 gin context 中的存取键。
const claimsKey = "orbitcloud.claims"

// AppGroup 聚合各模块接口(Auth/User/Bucket/File/Share/Group/Download/RecycleBin/Pprof),
// 各次级接口均为无状态空结构体,配合全局单例 App 使用。
type AppGroup struct {
	Auth       AuthAPI
	User       UserAPI
	Bucket     BucketAPI
	File       FileAPI
	Share      ShareAPI
	Group      GroupAPI
	Download   DownloadAPI
	RecycleBin RecycleBinAPI
	Pprof      PprofAPI
}

// App 是 api 层的全局单例,零值即可用。
var App AppGroup

// Router 注册全部模块路由并返回 *gin.Engine(实现 http.Handler,供 main.go 直接使用)。
// 中间件挂载策略(设计点:慢流与短请求隔离):
//   - 非流式接口(authed 组)→ 全局 ExecPoolMiddleware,经 core.ExecPool 执行,
//     池有界队列满时 SubmitCtx 阻塞 → 自然背压;
//   - 流式接口(streamAuthed 组 + 公开分享下载)→ 不经过执行池,handler 原地执行,
//     仅挂 StreamingPoolMiddleware 做准入令牌控制,防止慢流占满 ExecPool worker
//     挤占短请求(修复:原先全局挂载使慢下载仍占用执行池 worker)。
func Router() *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	// 运行模式与配置对齐
	if core.GlobalConfig != nil {
		gin.SetMode(core.GlobalConfig.Server.Mode)
		if core.GlobalConfig.Server.Mode == "debug" {
			r.Use(RequestLogMiddleware)
		}
	}

	// 公开路由组(无需鉴权):
	pub := r.Group("/api/v1")
	// 鉴权路由组:非流式接口经执行池(池满背压);流式接口单独分组,不占执行池。
	authed := r.Group("/api/v1")
	authed.Use(ExecPoolMiddleware)
	// 流式接口组(下载/预览/流媒体/批量下载):handler 原地执行,
	// 仅由各路由上的 StreamingPoolMiddleware 做准入令牌控制。
	streamAuthed := r.Group("/api/v1")

	// 各模块路由注册(路由清单见对应 router_*.go):
	AuthRouter(pub, authed)
	UserRouter(authed)
	GroupRouter(authed)
	BucketRouter(authed)
	FileRouter(authed, streamAuthed)
	ShareRouter(pub, authed)
	DownloadRouter(authed)
	RecycleBinRouter(authed)
	// pprof 管理接口(管理员,按需采样):走 streamAuthed 组,不占执行池
	PprofRouter(streamAuthed)
	return r
}

// ClaimsFrom 从 gin context 取鉴权 Claims;未鉴权路径调用返回 nil。
func ClaimsFrom(c *gin.Context) *core.Claims {
	v, ok := c.Get(claimsKey)
	if !ok {
		return nil
	}
	claims, ok := v.(*core.Claims)
	if !ok {
		return nil
	}
	return claims
}

// currentUser 返回当前登录用户 ID(假定已通过 AuthMiddleware;Claims 缺失时返回 0)。
func currentUser(c *gin.Context) uint {
	claims := ClaimsFrom(c)
	if claims == nil {
		return 0
	}
	return claims.UserID
}

// httpStatus 将哨兵错误映射为 HTTP 状态码。
func httpStatus(err error) int {
	return common.HTTPStatus(err)
}

// respondError 写出统一错误响应;500 内部错误统一为 "internal error",不暴露细节。
func respondError(c *gin.Context, err error) {
	msg := err.Error()
	if httpStatus(err) == http.StatusInternalServerError {
		msg = "internal error"
	}
	common.Error(c, httpStatus(err), msg)
}