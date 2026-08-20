// auth_api.go 认证模块接口:注册 / 登录 / 刷新 / 登出。
package api

import (
	"github.com/gin-gonic/gin"

	"orbitcloud/common"
	"orbitcloud/model"
	"orbitcloud/server"
)

// AuthAPI 认证模块接口(无状态):login/refresh 公开,register 需
// AuthMiddleware + AdminMiddleware,logout 需 AuthMiddleware。
type AuthAPI struct{}

// Register 管理员创建用户(POST /auth/register,普通用户不能自助注册)。
// 请求体:{"username","password","permission_level"?}。
func (AuthAPI) Register(c *gin.Context) {
	// 管理员校验由路由挂载的 AdminMiddleware 统一完成

	var req struct {
		Username        string                 `json:"username"`
		Password        string                 `json:"password"`
		PermissionLevel *model.PermissionLevel `json:"permission_level"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}

	// permission_level 缺省由 server 层归一为普通用户
	perm := model.NormalUser
	if req.PermissionLevel != nil {
		perm = *req.PermissionLevel
	}
	user, err := server.Register(c.Request.Context(), server.RegisterArg{Username: req.Username, Password: req.Password, PermissionLevel: perm})
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, user)
}

// Login 用户登录(POST /auth/login,公开),返回令牌对与用户信息。
func (AuthAPI) Login(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}

	res, err := server.Login(c.Request.Context(), server.LoginArg{Username: req.Username, Password: req.Password})
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, gin.H{
		"access_token":  res.AccessToken,
		"expires_in":    res.ExpiresIn,
		"refresh_token": res.RefreshToken,
		"user":          res.User,
	})
}

// Refresh 用刷新令牌换取新令牌对(POST /auth/refresh,公开),旧令牌即刻失效。
func (AuthAPI) Refresh(c *gin.Context) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}

	res, err := server.Refresh(c.Request.Context(), server.RefreshArg{RefreshToken: req.RefreshToken})
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, gin.H{
		"access_token":  res.AccessToken,
		"expires_in":    res.ExpiresIn,
		"refresh_token": res.RefreshToken,
		"user":          res.User,
	})
}

// Logout 吊销刷新令牌(POST /auth/logout,幂等,成功 204)。
func (AuthAPI) Logout(c *gin.Context) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}

	// 令牌已吊销/不存在同样视为成功
	if err := server.Logout(c.Request.Context(), server.LogoutArg{RefreshToken: req.RefreshToken}); err != nil {
		respondError(c, err)
		return
	}

	c.Status(204)
}
