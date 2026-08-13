// stream.go 文件流统一写出(下载/预览/分享三通道共用):响应头 + 200/206 语义。
// 区间合法性由各 handler 在打开流之前判定,本文件不做重复校验。
package api

import (
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"orbitcloud/common"
	"orbitcloud/model"
	"orbitcloud/utils"
)

// serveFileStream 按 Range 语义写出文件流(下载/预览/分享三通道共用)。
// 统一写响应头后流式拷贝:br 为 nil → 200 全量,否则 → 206 区间;
// rc 由本函数负责关闭。
func serveFileStream(c *gin.Context, rc io.ReadCloser, meta *model.File, disposition, contentType string, br *utils.ByteRange) {
	defer rc.Close() // 必须,防连接泄漏

	// 统一头
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	c.Header("Content-Type", contentType)
	c.Header("Content-Disposition", disposition)
	c.Header("Accept-Ranges", "bytes")

	// 全量 → 200
	if br == nil {
		c.Header("Content-Length", strconv.FormatInt(meta.FileSize, 10))
		c.Status(http.StatusOK)
		_, _ = io.Copy(c.Writer, rc)
		return
	}

	// 区间 → 206
	c.Header("Content-Range", fmt.Sprintf("bytes %d-%d/%d", br.Start, br.End, meta.FileSize))
	c.Header("Content-Length", strconv.FormatInt(br.Length(), 10))
	c.Status(http.StatusPartialContent)
	_, _ = io.Copy(c.Writer, rc)
}

// writeRangeNotSatisfiable 写出 416(ParseRange 失败/区间越界时调用),
// 响应体带 Content-Range: bytes */size,对齐 RFC 7233 §4.4。
func writeRangeNotSatisfiable(c *gin.Context, size int64) {
	c.Header("Accept-Ranges", "bytes")
	c.Header("Content-Range", fmt.Sprintf("bytes */%d", size))
	common.Error(c, http.StatusRequestedRangeNotSatisfiable, "range not satisfiable")
}
