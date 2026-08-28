// router_recycle_bin.go 回收站模块路由注册。
package api

import "github.com/gin-gonic/gin"

// RecycleBinRouter 注册回收站模块路由(全部需 AuthMiddleware)。
func RecycleBinRouter(authed *gin.RouterGroup) {
	authed.GET("/recycle-bin", AuthMiddleware, App.RecycleBin.ListRecycleBin)
}
