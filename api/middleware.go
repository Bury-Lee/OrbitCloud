// middleware.go 鉴权/管理员/请求日志中间件。
package api

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/gin-gonic/gin"

	"orbitcloud/common"
	"orbitcloud/log"
	"orbitcloud/server"
)

// AuthMiddleware 登录鉴权:校验 Authorization: Bearer <token>,通过后把 Claims
// 注入 gin context;失败一律 401。
func AuthMiddleware(c *gin.Context) {
	// 从请求头提取 token
	h := c.GetHeader("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		common.Unauthorized(c, "unauthorized")
		c.Abort()
		return
	}
	token := strings.TrimPrefix(h, "Bearer ")

	// 令牌无效/过期,统一 401
	claims, err := server.VerifyToken(c.Request.Context(), server.VerifyTokenArg{Token: token})
	if err != nil {
		common.Unauthorized(c, "unauthorized")
		c.Abort()
		return
	}

	// 注入 gin context
	c.Set(claimsKey, claims)
	c.Next()
}

// QueryTokenAuthMiddleware 查询参数令牌鉴权(流媒体接口专用):从 ?token= 校验
// access_token 并注入 Claims,与 AuthMiddleware 完全同权。
// 用于 HTML 媒体元素无法携带 Authorization 头的场景。
func QueryTokenAuthMiddleware(c *gin.Context) {
	token := c.Query("token")
	if token == "" {
		common.Unauthorized(c, "unauthorized")
		c.Abort()
		return
	}
	claims, err := server.VerifyToken(c.Request.Context(), server.VerifyTokenArg{Token: token})
	if err != nil {
		common.Unauthorized(c, "unauthorized")
		c.Abort()
		return
	}
	c.Set(claimsKey, claims)
	c.Next()
}

// AdminMiddleware 管理员校验(须在 AuthMiddleware 之后):Claims.PermissionLevel <= 1,否则 403。
func AdminMiddleware(c *gin.Context) {
	// 未鉴权 → 401
	claims := ClaimsFrom(c)
	if claims == nil {
		common.Unauthorized(c, "unauthorized")
		c.Abort()
		return
	}

	// 管理员 = 权限 <= 1
	if claims.PermissionLevel > 1 {
		common.Forbidden(c, "forbidden")
		c.Abort()
		return
	}

	c.Next()
}

// RequestLogMiddleware 打印完整请求信息(Header/Body),仅 debug 模式挂载。
func RequestLogMiddleware(c *gin.Context) {
	var logBuilder strings.Builder

	logBuilder.WriteString("\n== 收到新请求 ==\n")
	logBuilder.WriteString(fmt.Sprintf("Method: %s\n", c.Request.Method))
	logBuilder.WriteString(fmt.Sprintf("URL: %s\n", c.Request.URL.String()))
	logBuilder.WriteString(fmt.Sprintf("Remote Addr: %s\n", c.ClientIP()))

	logBuilder.WriteString("--- 请求头 ---\n")
	for key, values := range c.Request.Header {
		logBuilder.WriteString(fmt.Sprintf("%s: %s\n", key, strings.Join(values, ", ")))
	}

	// 读取并记录请求体,随后写回 Body 供后续 handler 读取
	var bodyBytes []byte
	var err error

	if c.Request.Body != nil {
		bodyBytes, err = io.ReadAll(c.Request.Body)
		if err != nil {
			log.Errorf("读取请求体失败: %v", err)
			c.Next()
			return
		}

		logBuilder.WriteString("--- 请求体 ---\n")
		if len(bodyBytes) > 0 {
			bodyStr := string(bodyBytes)
			if len(bodyStr) > 2000 {
				logBuilder.WriteString(fmt.Sprintf("%s\n... (内容过长，已截断) 长度: %d\n", bodyStr[:2000], len(bodyStr)))
			} else {
				logBuilder.WriteString(fmt.Sprintf("%s\n", bodyStr))
			}
		} else {
			logBuilder.WriteString("(空)\n")
		}

		// 将已读数据写回 Body,否则后续 gin handler 无法读取
		c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	} else {
		logBuilder.WriteString("--- 请求体 ---\n")
		logBuilder.WriteString("(无请求体)\n")
	}

	logBuilder.WriteString("============================")

	log.Debug(logBuilder.String())

	c.Next()
}
