// system_api.go —— 运维/系统接口(仅管理员):运行状况查询、日志文件浏览与下载。
package api

import (
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"orbitcloud/common"
	"orbitcloud/server"
)

// SystemAPI 系统运维模块接口(无状态)。
type SystemAPI struct{}

// Status 系统运行状况(GET /system/status,管理员):启动时间/运行时长、
// Go 运行时、DB 与对象存储连通性。
func (SystemAPI) Status(c *gin.Context) {
	status, err := server.SystemStatus(c.Request.Context(), server.SystemStatusArg{})
	if err != nil {
		respondError(c, err)
		return
	}
	common.Success(c, status)
}

// ListLogs 日志文件列表(GET /logs,管理员):返回日期目录 + 文件(大小/修改时间)。
func (SystemAPI) ListLogs(c *gin.Context) {
	infos, err := server.ListLogFiles(c.Request.Context(), nil)
	if err != nil {
		respondError(c, err)
		return
	}
	common.Success(c, gin.H{"files": infos})
}

// RateLimitStats 限流状态与记录(GET /system/ratelimit,管理员):
// 返回启用状态/速率配置/被限流请求记录(IP/账号/路径,最新在前)。
func (SystemAPI) RateLimitStats(c *gin.Context) {
	if server.RateLimit == nil {
		common.Success(c, gin.H{"enabled": false, "records": []server.RateLimitRecord{}})
		return
	}
	common.Success(c, gin.H{
		"stats":   server.RateLimit.Stats(),
		"records": server.RateLimit.Records(),
	})
}

// DownloadLog 下载日志文件(GET /logs/download?path=相对路径,管理员):
// 路径限日志根内且仅 .log 文件(防目录穿越),流式返回。
func (SystemAPI) DownloadLog(c *gin.Context) {
	var req struct {
		Path string `form:"path"` // 相对日志根的路径
	}
	if !bindRequest(c, &req) {
		return
	}
	path := req.Path
	if path == "" {
		respondError(c, server.ErrInvalidInput)
		return
	}

	rc, info, err := server.GetLogFile(c.Request.Context(), server.GetLogFileArg{Path: path})
	if err != nil {
		respondError(c, err)
		return
	}
	defer rc.Close()

	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Header("Content-Length", strconv.FormatInt(info.Size, 10))
	c.Header("Content-Disposition", "attachment; filename="+info.Name)
	// 关键:gin render.Reader 用 io.Copy(读到 EOF),而日志文件下载期间仍在
	// 持续写入增长——直接传 rc 会读满增长后的文件,实际写入超过声明的
	// Content-Length → "wrote more than the declared Content-Length" + 响应损坏。
	// 用 LimitReader 锁定读取上限 = Stat 时大小,写入恒等于 CL。
	c.DataFromReader(http.StatusOK, info.Size, "text/plain; charset=utf-8", io.LimitReader(rc, info.Size), nil)
}
