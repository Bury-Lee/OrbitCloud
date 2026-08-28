// file_search.go —— 文件/文件夹前缀搜索接口。
package api

import (
	"github.com/gin-gonic/gin"

	"orbitcloud/common"
	"orbitcloud/server"
)

// fileSearchReq 搜索接口通用参数(路径参数 + 查询参数统一绑定)。
type fileSearchReq struct {
	bucketPathReq
	Path     string `form:"path"`      // 在该路径下匹配(如果有)
	FolderID uint   `form:"folder_id"` // path=="" 时使用该 ID 匹配文件夹;0 = 全桶前缀匹配
	Key      string `form:"key"`       // 搜索前缀
	Page     int    `form:"page"`      // 页码(默认 1)
	PageSize int    `form:"page_size"` // 页大小(默认 50)
}

func (FileAPI) FileSearch(c *gin.Context) {
	var req fileSearchReq
	if !bindRequest(c, &req) {
		return
	}
	bucketID := req.BucketID
	userID := currentUser(c)
	ctx := c.Request.Context()

	if _, err := precheckBucketRead(ctx, userID, bucketID); err != nil {
		respondError(c, err)
		return
	}

	files, err := server.FileSearch(ctx, server.FileSearchArg{
		UserID:   userID,
		BucketID: bucketID,
		Path:     req.Path,
		FolderID: req.FolderID,
		Key:      req.Key,
		Page:     req.Page,
		PageSize: req.PageSize,
	})
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, gin.H{"files": files})
}

func (FileAPI) FolderSearch(c *gin.Context) {
	var req fileSearchReq
	if !bindRequest(c, &req) {
		return
	}
	bucketID := req.BucketID
	userID := currentUser(c)
	ctx := c.Request.Context()

	if _, err := precheckBucketRead(ctx, userID, bucketID); err != nil {
		respondError(c, err)
		return
	}

	folders, err := server.FolderSearch(ctx, server.FolderSearchArg{
		UserID:   userID,
		BucketID: bucketID,
		Path:     req.Path,
		FolderID: req.FolderID,
		Key:      req.Key,
		Page:     req.Page,
		PageSize: req.PageSize,
	})
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, gin.H{"folders": folders})
}