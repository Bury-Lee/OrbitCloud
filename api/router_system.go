// router_system.go 系统运维模块路由注册(全部需 AuthMiddleware + AdminMiddleware)。
package api

import "github.com/gin-gonic/gin"

// SystemRouter 注册系统运维路由(管理员):状况查询、日志浏览/下载。
func SystemRouter(authed *gin.RouterGroup) {
	admin := authed.Group("")
	admin.Use(AdminMiddleware)
	{
		admin.GET("/system/status", AuthMiddleware, App.System.Status)
		admin.GET("/system/ratelimit", AuthMiddleware, App.System.RateLimitStats)
		admin.GET("/logs", AuthMiddleware, App.System.ListLogs)
		admin.GET("/logs/download", AuthMiddleware, App.System.DownloadLog)
	}
}
