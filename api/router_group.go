// router_group.go 用户组模块路由注册。
// 组管理接口为管理员专属,查询接口对组内成员开放。
package api

import "github.com/gin-gonic/gin"

// GroupRouter 注册用户组模块路由(全部需 AuthMiddleware)。
func GroupRouter(authed *gin.RouterGroup) {
	// 我的组(任意登录用户):
	authed.GET("/users/me/groups", AuthMiddleware, App.Group.ListMyGroups)
	// 组管理(管理员专属:创建/修改/删除/加人/踢人):
	authed.POST("/groups", AuthMiddleware, AdminMiddleware, App.Group.CreateGroup)
	authed.GET("/groups", AuthMiddleware, AdminMiddleware, App.Group.ListGroups)
	authed.GET("/groups/:id", AuthMiddleware, App.Group.GetGroup) // 管理员或组内成员
	authed.PUT("/groups/:id", AuthMiddleware, AdminMiddleware, App.Group.UpdateGroup)
	authed.DELETE("/groups/:id", AuthMiddleware, AdminMiddleware, App.Group.DeleteGroup)
	authed.POST("/groups/:id/members", AuthMiddleware, AdminMiddleware, App.Group.AddGroupMember)
	authed.DELETE("/groups/:id/members/:uid", AuthMiddleware, AdminMiddleware, App.Group.RemoveGroupMember)
	authed.GET("/groups/:id/members", AuthMiddleware, App.Group.ListGroupMembers) // 管理员或组内成员
}
