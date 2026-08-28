// group_api.go 用户组模块接口:组 CRUD / 成员管理 / 我的组。
// 组管理接口为管理员专属;组详情与成员列表对组内成员开放。
package api

import (

	"github.com/gin-gonic/gin"

	"orbitcloud/common"
	"orbitcloud/server"
)

// GroupAPI 用户组模块接口(无状态)。
type GroupAPI struct{}

// groupIDReq /groups/:id 路径参数。
type groupIDReq struct {
	GroupID uint `uri:"id" form:"-"`
}

// groupMembersReq /groups/:id/members/:uid 路径参数。
type groupMembersReq struct {
	groupIDReq
	UserID uint `uri:"uid" form:"-"`
}

// groupPageReq 组列表分页参数(路径 + 分页)。
type groupPageReq struct {
	groupIDReq
	Page     int `form:"page"`
	PageSize int `form:"page_size"`
}

// CreateGroup 创建用户组(POST /groups,管理员)。
// 请求体:{"name","description"?};组为纯可见组白名单,无权限等级字段。
func (GroupAPI) CreateGroup(c *gin.Context) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}
	group, err := server.CreateGroup(c.Request.Context(), server.CreateGroupArg{OperatorID: currentUser(c), Name: req.Name, Description: req.Description})
	if err != nil {
		respondError(c, err)
		return
	}
	common.Success(c, group)
}

// ListGroups 分页列出用户组(GET /groups?page=&page_size=,管理员)。
func (GroupAPI) ListGroups(c *gin.Context) {
	var req struct {
		Page     int `form:"page"`
		PageSize int `form:"page_size"`
	}
	if !bindRequest(c, &req) {
		return
	}
	total, items, err := server.ListGroups(c.Request.Context(), server.ListGroupsArg{UserID: currentUser(c), Page: req.Page, PageSize: req.PageSize})
	if err != nil {
		respondError(c, err)
		return
	}
	common.Success(c, gin.H{"total": total, "items": items})
}

// GetGroup 返回组信息(GET /groups/:id,管理员或组内成员)。
func (GroupAPI) GetGroup(c *gin.Context) {
	var req groupIDReq
	if !bindRequest(c, &req) {
		return
	}
	groupID := req.GroupID
	ctx := c.Request.Context()
	userID := currentUser(c)

	// 组存在 → 组成员可见性(管理员或组内成员)
	group, err := server.GetGroup(ctx, server.GetGroupArg{GroupID: groupID})
	if err != nil {
		respondError(c, err)
		return
	}
	if err := permGroupVisible(ctx, userID, groupID); err != nil {
		respondError(c, err)
		return
	}
	common.Success(c, group)
}

// UpdateGroup 更新用户组(PUT /groups/:id,管理员)。
// 请求体:{"name"?,"description"?,"status"?}(非空字段才更新)。
func (GroupAPI) UpdateGroup(c *gin.Context) {
	var req struct {
		groupIDReq
		Name        string `json:"name"`
		Description string `json:"description"`
		Status      *int8  `json:"status"`
	}
	if !bindRequest(c, &req) {
		return
	}
	groupID := req.GroupID
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}
	group, err := server.UpdateGroup(c.Request.Context(), server.UpdateGroupArg{
		OperatorID: currentUser(c), GroupID: groupID,
		Name: req.Name, Description: req.Description, Status: req.Status,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	common.Success(c, group)
}

// DeleteGroup 删除用户组(DELETE /groups/:id,管理员,软删,成功 204)。
func (GroupAPI) DeleteGroup(c *gin.Context) {
	var req groupIDReq
	if !bindRequest(c, &req) {
		return
	}
	if err := server.DeleteGroup(c.Request.Context(), server.DeleteGroupArg{OperatorID: currentUser(c), GroupID: req.GroupID}); err != nil {
		respondError(c, err)
		return
	}
	c.Status(204)
}

// AddGroupMember 添加组成员(POST /groups/:id/members,管理员);请求体:{"user_id"}。
func (GroupAPI) AddGroupMember(c *gin.Context) {
	var req struct {
		groupIDReq
		UserID uint `json:"user_id"`
	}
	if !bindRequest(c, &req) {
		return
	}
	groupID := req.GroupID
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}
	if req.UserID == 0 {
		respondError(c, server.ErrInvalidInput)
		return
	}
	if err := server.AddGroupMember(c.Request.Context(), server.AddGroupMemberArg{OperatorID: currentUser(c), GroupID: groupID, UserID: req.UserID}); err != nil {
		respondError(c, err)
		return
	}
	common.Success(c, gin.H{"group_id": groupID, "user_id": req.UserID})
}

// RemoveGroupMember 移除组成员(DELETE /groups/:id/members/:uid,管理员,成功 204)。
func (GroupAPI) RemoveGroupMember(c *gin.Context) {
	var req groupMembersReq
	if !bindRequest(c, &req) {
		return
	}
	groupID, userID := req.GroupID, req.UserID
	if err := server.RemoveGroupMember(c.Request.Context(), server.RemoveGroupMemberArg{OperatorID: currentUser(c), GroupID: groupID, UserID: userID}); err != nil {
		respondError(c, err)
		return
	}
	c.Status(204)
}

// ListGroupMembers 分页返回组成员(GET /groups/:id/members,管理员或组内成员)。
func (GroupAPI) ListGroupMembers(c *gin.Context) {
	var req struct {
		groupIDReq
		Page     int `form:"page"`
		PageSize int `form:"page_size"`
	}
	if !bindRequest(c, &req) {
		return
	}
	groupID := req.GroupID
	ctx := c.Request.Context()
	userID := currentUser(c)

	// 权限预检:组存在(可行性)+ 组成员可见性
	if _, err := server.GetGroup(ctx, server.GetGroupArg{GroupID: groupID}); err != nil {
		respondError(c, err)
		return
	}
	if err := permGroupVisible(ctx, userID, groupID); err != nil {
		respondError(c, err)
		return
	}

	total, items, err := server.ListGroupMembers(ctx, server.ListGroupMembersArg{GroupID: groupID, Page: req.Page, PageSize: req.PageSize})
	if err != nil {
		respondError(c, err)
		return
	}
	common.Success(c, gin.H{"total": total, "items": items})
}

// ListMyGroups 返回当前用户所属的全部正常组(GET /users/me/groups)。
func (GroupAPI) ListMyGroups(c *gin.Context) {
	groups, err := server.ListMyGroups(c.Request.Context(), server.ListMyGroupsArg{UserID: currentUser(c)})
	if err != nil {
		respondError(c, err)
		return
	}
	common.Success(c, groups)
}
