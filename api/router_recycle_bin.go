// router_recycle_bin.go 回收站模块路由注册。
package api

import "github.com/gin-gonic/gin"

// RecycleBinRouter 注册回收站模块路由(全部需 AuthMiddleware)。
func RecycleBinRouter(authed *gin.RouterGroup) {
	authed.GET("/recycle-bin/files", AuthMiddleware, App.RecycleBin.ListRecycleFile)
	authed.GET("/recycle-bin/folders", AuthMiddleware, App.RecycleBin.ListRecycleFolder)
	authed.DELETE("/recycle-bin/files/:id", AuthMiddleware, App.RecycleBin.DeleteRecycleFile)
	authed.POST("/recycle-bin/files/:id/restore", AuthMiddleware, App.RecycleBin.RestoreRecycleFile)
}
