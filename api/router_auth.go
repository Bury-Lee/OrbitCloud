// router_auth.go 认证模块路由注册。
package api

import "github.com/gin-gonic/gin"

// AuthRouter 注册认证模块路由:login/refresh 公开,register(管理员)/logout 需鉴权。
func AuthRouter(pub, authed *gin.RouterGroup) {
	// 公开路由组:
	pub.POST("/auth/login", App.Auth.Login)
	pub.POST("/auth/refresh", App.Auth.Refresh)
	// 鉴权路由组:
	authed.POST("/auth/register", AuthMiddleware, AdminMiddleware, App.Auth.Register)
	authed.POST("/auth/logout", AuthMiddleware, App.Auth.Logout)
}
