// user_api.go 用户模块接口:当前用户 / 修改 / 列表 / 修改他人 / 删除。
// 管理员判定:PermissionLevel <= 1(0=超级管理员,1=管理员)。
package api

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"orbitcloud/common"
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
func (UserAPI) ListUsers(c *gin.Context) {
	// 管理员校验:权限 <= 1
	claims := ClaimsFrom(c)
	if claims == nil || claims.PermissionLevel > 1 {
		respondError(c, server.ErrForbidden)
		return
	}

	// 分页参数(默认 1 / 50,非法值回退默认)
	page := queryInt(c, "page", 1)
	pageSize := queryInt(c, "page_size", 50)

	total, items, err := server.ListUsers(c.Request.Context(), server.ListUsersArg{Page: page, PageSize: pageSize})
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, gin.H{"total": total, "items": items})
}

// UpdateUser 管理员修改指定用户(PUT /users/:id)。
// 请求体:{"password"?,"name"?,"email"?,"permission_level"?,"status"?}(指针字段才更新);
// 不能操作同级或更高级用户。
func (UserAPI) UpdateUser(c *gin.Context) {
	// 解析路径参数 :id
	targetID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || targetID == 0 {
		respondError(c, server.ErrInvalidInput)
		return
	}
	ctx := c.Request.Context()
	operatorID := currentUser(c)

	// 管理员校验:权限 <= 1
	claims := ClaimsFrom(c)
	if claims == nil || claims.PermissionLevel > 1 {
		respondError(c, server.ErrForbidden)
		return
	}

	// 目标存在,且操作者权限须严格高于目标
	target, err := server.GetUser(ctx, server.GetUserArg{ID: uint(targetID)})
	if err != nil {
		respondError(c, err)
		return
	}
	if claims.PermissionLevel >= target.PermissionLevel {
		respondError(c, server.ErrForbidden)
		return
	}

	// 解析请求体(本地 DTO 带 json tag,避免 server 层结构体直接绑定)
	var req struct {
		Password        string `json:"password"`
		Name            string `json:"name"`
		Email           string `json:"email"`
		PermissionLevel *int8  `json:"permission_level"`
		Status          *int   `json:"status"`
	}
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
	}

	user, err := server.UpdateUser(ctx, server.UpdateUserArg{OperatorID: operatorID, TargetID: uint(targetID), In: in})
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, user)
}

// DeleteUser 管理员删除指定用户(DELETE /users/:id,软删,成功 204);
// 不能删除同级或更高级用户。
func (UserAPI) DeleteUser(c *gin.Context) {
	// 管理员校验:权限 <= 1
	claims := ClaimsFrom(c)
	if claims == nil || claims.PermissionLevel > 1 {
		respondError(c, server.ErrForbidden)
		return
	}

	// 解析路径参数 :id
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		respondError(c, server.ErrInvalidInput)
		return
	}
	ctx := c.Request.Context()
	operatorID := currentUser(c)

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

// queryInt 读取查询参数 key 的整数值,不存在或非法时返回 defaultVal。
func queryInt(c *gin.Context, key string, defaultVal int) int {
	s := c.Query(key)
	if s == "" {
		return defaultVal
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 {
		return defaultVal
	}
	return v
}
