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

type ListRecycleBinArg struct {
	common.PageInfo      //查询参数
	UserID          uint //非管理员只可查询自己的
}

// ListRecycleBin 分页返回自己的回收站(GET /recycle-bin?page=&page_size=&cursor=&key=&order=):
// 已删除(软删)且归属自己的文件,deleted_at 倒序;走 common 统一分页封装
// (ParseOption 解析查询参数构造 Option,WrapPageResponse 输出 PaginatedResult,
// 与后续回收站其他接口共用同一分页形态)。
func (RecycleBinAPI) ListRecycleBin(c *gin.Context) {
	res, err := server.ListRecycleBin(c.Request.Context(), server.ListRecycleBinArg{
		UserID: currentUser(c),
		Opt:    common.ParseOption(c.Request.URL.Query()),
	})
	if err != nil {
		respondError(c, err)
		return
	}

	common.WrapPageResponse(c, res)
}
