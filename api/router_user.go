// router_user.go 用户模块路由注册。
package api

import "github.com/gin-gonic/gin"

// UserRouter 注册用户模块路由(全部需 AuthMiddleware;列表/改他人/删除另需 AdminMiddleware)。
func UserRouter(authed *gin.RouterGroup) {
	// 当前用户(需 AuthMiddleware):
	authed.GET("/users/me", AuthMiddleware, App.User.Me)
	authed.PUT("/users/me", AuthMiddleware, App.User.UpdateMe)
	// 管理员专属(需 AuthMiddleware + AdminMiddleware):
	authed.GET("/users", AuthMiddleware, AdminMiddleware, App.User.ListUsers)
	authed.PUT("/users/:id", AuthMiddleware, AdminMiddleware, App.User.UpdateUser)
	authed.DELETE("/users/:id", AuthMiddleware, AdminMiddleware, App.User.DeleteUser)
}
