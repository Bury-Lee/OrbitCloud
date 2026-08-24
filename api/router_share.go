// router_share.go 分享模块路由注册。
package api

import "github.com/gin-gonic/gin"

// ShareRouter 注册分享模块路由:解析/下载公开(经 token/提取码/有效期/次数校验),
// 创建/列表/修改/删除需鉴权。公开下载为流式接口,挂 StreamingPool 准入
// (不经过执行池,防慢流占 worker)。
func ShareRouter(pub, authed *gin.RouterGroup) {
	// 公开路由组:
	pub.GET("/share/:token", StreamingPoolMiddleware, App.Share.ResolveShare)
	// 鉴权路由组:
	authed.POST("/shares", AuthMiddleware, App.Share.CreateShare)
	authed.GET("/shares", AuthMiddleware, App.Share.ListShares)
	authed.PUT("/shares/:id", AuthMiddleware, App.Share.UpdateShare)
	authed.DELETE("/shares/:id", AuthMiddleware, App.Share.DeleteShare)
}
