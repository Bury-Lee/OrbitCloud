// recycle_bin_api.go —— 回收站接口:查询自己的回收站(已删除且归属自己的文件)。
// 删除/清除见 file_delete.go 与 server 回收站清理;本模块只做回收站浏览。
package api

import (
	"github.com/gin-gonic/gin"

	"orbitcloud/common"
	"orbitcloud/server"
)

// RecycleBinAPI 回收站模块接口(无状态)。
type RecycleBinAPI struct{}

// ListRecycleBin 分页返回自己的回收站(GET /recycle-bin?page=&page_size=):
// 已删除(软删)且归属自己的文件,deleted_at 倒序。
func (RecycleBinAPI) ListRecycleBin(c *gin.Context) {
	page := queryInt(c, "page", 1)
	pageSize := queryInt(c, "page_size", 50)

	total, items, err := server.ListRecycleBin(c.Request.Context(), server.ListRecycleBinArg{UserID: currentUser(c), Page: page, PageSize: pageSize})
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, gin.H{"total": total, "items": items})
}
