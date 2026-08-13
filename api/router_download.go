// router_download.go 下载任务模块路由注册。
// 下载本体走文件下载接口(支持 Range),本模块只做任务登记/查询/清理。
package api

import "github.com/gin-gonic/gin"

// DownloadRouter 注册下载任务模块路由(全部需 AuthMiddleware)。
func DownloadRouter(authed *gin.RouterGroup) {
	authed.POST("/download-tasks", AuthMiddleware, App.Download.CreateDownloadTask)
	authed.GET("/download-tasks/:id", AuthMiddleware, App.Download.GetDownloadTask)
	authed.DELETE("/download-tasks/:id", AuthMiddleware, App.Download.CompleteDownloadTask)
}
