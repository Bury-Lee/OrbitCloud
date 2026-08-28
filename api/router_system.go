// router_system.go 系统运维模块路由注册(全部需 AuthMiddleware + AdminMiddleware)。
package api

import "github.com/gin-gonic/gin"

// SystemRouter 注册系统运维路由(管理员):状况查询、限流状态、日志浏览/下载。
// 中间件顺序:AuthMiddleware 必须先于 AdminMiddleware(组级参数按序执行,
// 否则 Admin 取不到 claims 恒 401)。
func SystemRouter(authed *gin.RouterGroup) {
	admin := authed.Group("", AuthMiddleware, AdminMiddleware)
	{
		admin.GET("/system/status", App.System.Status)
		admin.GET("/system/ratelimit", App.System.RateLimitStats)
		admin.GET("/logs", App.System.ListLogs)
		admin.GET("/logs/download", App.System.DownloadLog)
	}
}
