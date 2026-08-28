// user_api.go 用户模块接口:当前用户 / 修改 / 列表 / 修改他人 / 删除。
// 管理员判定:PermissionLevel.IsAdmin()(0=超级管理员,1=管理员)。
package api

import (

	"github.com/gin-gonic/gin"

	"orbitcloud/common"
	"orbitcloud/model"
	"orbitcloud/server"
)

// UserAPI 用户模块接口(无状态):me/修改需 AuthMiddleware,
// 列表/改他人/删除需 AuthMiddleware + AdminMiddleware。
type UserAPI struct{}

// Me 返回当前登录用户信息(GET /users/me,基于 Claims.UserID)。
func (UserAPI) Me(c *gin.Context) {
	// 鉴权中间件已确保 Claims 非零
	userID := currentUser(c)

	user, err := server.GetUser(c.Request.Context(), server.GetUserArg{ID: userID})
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, user)
}

// UpdateMe 更新当前用户资料(PUT /users/me)。
// 请求体:{"password"?,"name"?,"email"?}(非空字段才更新);不接受权限等级/状态字段。
func (UserAPI) UpdateMe(c *gin.Context) {
	// 仅绑定 Password/Name/Email 字段
	var req server.UpdateMeInput
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}

	user, err := server.UpdateMe(c.Request.Context(), server.UpdateMeArg{UserID: currentUser(c), In: req})
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, user)
}

// ListUsers 管理员分页列出用户(GET /users?page=&page_size=);非管理员 403。
// 管理员判定由路由挂载的 AdminMiddleware 统一完成。
func (UserAPI) ListUsers(c *gin.Context) {
	var req struct {
		Page     int `form:"page"`
		PageSize int `form:"page_size"`
	}
	if !bindRequest(c, &req) {
		return
	}

	total, items, err := server.ListUsers(c.Request.Context(), server.ListUsersArg{Page: req.Page, PageSize: req.PageSize})
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, gin.H{"total": total, "items": items})
}

// UpdateUser 管理员修改指定用户(PUT /users/:id)。
// 请求体:{"password"?,"name"?,"email"?,"permission_level"?,"status"?,"quota"?}
// (指针字段才更新);quota 三态:缺省 = 不更新,null = 置为不限,0 = 仅可读,>0 = 软字节限额;
// 不能操作同级或更高级用户。管理员判定由路由挂载的 AdminMiddleware 统一完成。
func (UserAPI) UpdateUser(c *gin.Context) {
	var req struct {
		TargetID        uint                   `uri:"id" form:"-"`
		Password        string                 `json:"password"`
		Name            string                 `json:"name"`
		Email           string                 `json:"email"`
		PermissionLevel *model.PermissionLevel `json:"permission_level"`
		Status          *int                   `json:"status"`
		Quota           *int64                 `json:"quota"` // 设置配额(0=仅可读,>0=限额;置回不限走 PUT /users/:id/quota)
	}
	if !bindRequest(c, &req) {
		return
	}
	targetID := req.TargetID
	ctx := c.Request.Context()
	operatorID := currentUser(c)
	// 操作者等级(严格高于目标校验用;管理员身份已由 AdminMiddleware 保证)
	claims := ClaimsFrom(c)

	// 目标存在,且操作者权限须严格高于目标
	target, err := server.GetUser(ctx, server.GetUserArg{ID: targetID})
	if err != nil {
		respondError(c, err)
		return
	}
	if claims.PermissionLevel >= target.PermissionLevel {
		respondError(c, server.ErrForbidden)
		return
	}

	// 解析请求体(本地 DTO 带 json tag,避免 server 层结构体直接绑定)
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}

	// 组装业务层入参(指针字段原样透传,缺失字段留 nil)
	in := server.UpdateUserInput{
		Password:        req.Password,
		Name:            req.Name,
		Email:           req.Email,
		PermissionLevel: req.PermissionLevel,
		Status:          req.Status,
		Quota:           req.Quota,
	}

	user, err := server.UpdateUser(ctx, server.UpdateUserArg{OperatorID: operatorID, TargetID: uint(targetID), In: in})
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, user)
}

// UpdateUserQuota 管理员设置用户配额(PUT /users/:id/quota)。
// 请求体:{"quota": <int64|null>}(null = 不限;0 = 仅可读;>0 = 软字节限额)。
// 与 DB 语义一致:quota 即最终值;不能操作同级或更高级用户(AdminMiddleware 已保证管理员)。
func (UserAPI) UpdateUserQuota(c *gin.Context) {
	var req struct {
		TargetID uint   `uri:"id" form:"-"`
		Quota    *int64 `json:"quota"` // null = 不限;0 = 仅可读;>0 = 限额
	}
	if !bindRequest(c, &req) {
		return
	}
	targetID := req.TargetID
	ctx := c.Request.Context()
	operatorID := currentUser(c)
	claims := ClaimsFrom(c)

	// 目标存在,且操作者权限须严格高于目标
	target, err := server.GetUser(ctx, server.GetUserArg{ID: targetID})
	if err != nil {
		respondError(c, err)
		return
	}
	if claims.PermissionLevel >= target.PermissionLevel {
		respondError(c, server.ErrForbidden)
		return
	}

	// 请求体仅一个字段:null → nil(不限);0 → 仅可读;>0 → 限额
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}

	user, err := server.UpdateUserQuota(ctx, server.UpdateUserQuotaArg{
		OperatorID: operatorID, TargetID: targetID, Quota: req.Quota,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	common.Success(c, user)
}

// DeleteUser 管理员删除指定用户(DELETE /users/:id,软删,成功 204);
// 不能删除同级或更高级用户。管理员判定由路由挂载的 AdminMiddleware 统一完成。
func (UserAPI) DeleteUser(c *gin.Context) {
	var req struct {
		TargetID uint `uri:"id" form:"-"`
	}
	if !bindRequest(c, &req) {
		return
	}
	id := req.TargetID
	ctx := c.Request.Context()
	operatorID := currentUser(c)
	// 操作者等级(严格高于目标校验用;管理员身份已由 AdminMiddleware 保证)
	claims := ClaimsFrom(c)

	// 目标存在,且操作者权限须严格高于目标
	target, err := server.GetUser(ctx, server.GetUserArg{ID: uint(id)})
	if err != nil {
		respondError(c, err)
		return
	}
	if claims.PermissionLevel >= target.PermissionLevel {
		respondError(c, server.ErrForbidden)
		return
	}

	if err := server.DeleteUser(ctx, server.DeleteUserArg{OperatorID: operatorID, TargetID: uint(id)}); err != nil {
		respondError(c, err)
		return
	}

	c.Status(204)
}
