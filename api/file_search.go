// file_search.go —— 文件/文件夹前缀搜索接口。
package api

import (
	"github.com/gin-gonic/gin"

	"orbitcloud/common"
	"orbitcloud/server"
)

type FileSearchArg struct {
	common.PageInfo
	BuketID  uint   //来自哪个桶
	Path     string //在该路径下匹配(如果有)
	FolderID uint   //在path==""时使用该ID来匹配文件夹,为0时全桶前缀匹配
}

func (FileAPI) FileSearch(c *gin.Context) {
	bucketID := parseIDParam(c, "id")
	if bucketID == 0 {
		return
	}
	userID := currentUser(c)
	ctx := c.Request.Context()

	if _, err := precheckBucketRead(ctx, userID, bucketID); err != nil {
		respondError(c, err)
		return
	}

	files, err := server.FileSearch(ctx, server.FileSearchArg{
		UserID:   userID,
		BucketID: bucketID,
		Path:     c.Query("path"),
		FolderID: uint(queryInt(c, "folder_id", 0)),
		Key:      c.Query("key"),
		Page:     queryInt(c, "page", 1),
		PageSize: queryInt(c, "page_size", 50),
	})
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, gin.H{"files": files})
}

type FolderSearchArg struct {
	common.PageInfo
	BuketID  uint   //来自哪个桶
	Path     string //在该路径下匹配(如果有)
	FolderID uint   //在path==""时使用该ID来匹配文件夹,为0时全桶前缀匹配
}

func (FileAPI) FolderSearch(c *gin.Context) {
	bucketID := parseIDParam(c, "id")
	if bucketID == 0 {
		return
	}
	userID := currentUser(c)
	ctx := c.Request.Context()

	if _, err := precheckBucketRead(ctx, userID, bucketID); err != nil {
		respondError(c, err)
		return
	}

	folders, err := server.FolderSearch(ctx, server.FolderSearchArg{
		UserID:   userID,
		BucketID: bucketID,
		Path:     c.Query("path"),
		FolderID: uint(queryInt(c, "folder_id", 0)),
		Key:      c.Query("key"),
		Page:     queryInt(c, "page", 1),
		PageSize: queryInt(c, "page_size", 50),
	})
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, gin.H{"folders": folders})
}