// file_batch_api.go 文件模块批量操作接口:批量删除 / 复制 / 移动。
// 调用 server 批量函数前在 api 层对所有条目做可见性预检,失败条目进 failed 列表。
package api

import (
	"context"
	"net/http"

	agilepool "github.com/Yiming1997/agilePool/v2"
	"github.com/gin-gonic/gin"

	"orbitcloud/core"
	"orbitcloud/server"
)

// BatchDelete 批量删除(POST /buckets/:id/items/batch-delete)。
// 请求体:{"items":[{"kind","id"}...]};返回 {success, failed}(部分失败也 200)。
func (FileAPI) BatchDelete(c *gin.Context) {
	var req struct {
		bucketPathReq
		Items []server.DeleteItem `json:"items"`
	}
	if !bindRequest(c, &req) {
		return
	}
	bucketID := req.BucketID
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}
	if err := server.CheckBatchItems(c.Request.Context(), server.CheckBatchItemsArg{Count: len(req.Items)}); err != nil {
		respondError(c, err)
		return
	}
	userID := currentUser(c)

	// 桶可写 + 条目存在性 + 条目级/祖先链可见性(api 层完成)
	if _, err := precheckBucketWrite(c.Request.Context(), userID, bucketID); err != nil {
		respondError(c, err)
		return
	}
	failed := precheckItems(c, userID, bucketID,
		func(i int) (string, uint) { return req.Items[i].Kind, req.Items[i].ID }, len(req.Items))
	if len(failed) > 0 {
		respondResult(c, nil, failed)
		return
	}

	results := server.DeleteItems(c.Request.Context(), server.DeleteItemsArg{
		UserID:   userID,
		BucketID: bucketID,
		Items:    req.Items,
	})

	// 文件夹项落后台任务表:统一经全局协程池提交物理清理(SubmitCtx 传请求上下文,
	// 客户端断开则跳过提交,任务由启动/cron 续跑)
	for i := range results {
		if results[i].TaskID == 0 {
			continue
		}
		taskID := results[i].TaskID
		core.Pool.SubmitCtx(c, agilepool.TaskFunc(func() error {
			return server.ProcessDeleteTask(context.Background(), server.ProcessDeleteTaskArg{TaskID: taskID})
		}))
	}

	respondResult(c, results, nil)
}

// BatchCopy 批量复制(POST /buckets/:id/items/batch-copy)。
// 请求体:{"dst_bucket_id","dst_dir"?,"items":[{"kind","id","dst_name"?}...]};
// 文件项返回新对象,文件夹项返回预建顶层。
func (FileAPI) BatchCopy(c *gin.Context) {
	var req struct {
		bucketPathReq
		DstBucketID uint              `json:"dst_bucket_id"`
		DstDir      string            `json:"dst_dir"`
		Items       []server.CopyItem `json:"items"`
	}
	if !bindRequest(c, &req) {
		return
	}
	srcBucketID := req.BucketID
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}
	if req.DstBucketID == 0 {
		respondError(c, server.ErrInvalidInput)
		return
	}
	if err := server.CheckBatchItems(c.Request.Context(), server.CheckBatchItemsArg{Count: len(req.Items)}); err != nil {
		respondError(c, err)
		return
	}
	userID := currentUser(c)

	// 源条目可读 + 目标桶可写
	failed := precheckItems(c, userID, srcBucketID,
		func(i int) (string, uint) { return req.Items[i].Kind, req.Items[i].SrcID }, len(req.Items))
	if len(failed) > 0 {
		respondResult(c, nil, failed)
		return
	}
	if _, err := precheckBucketWrite(c.Request.Context(), userID, req.DstBucketID); err != nil {
		respondError(c, err)
		return
	}

	results := server.CopyItems(c.Request.Context(), server.CopyItemsArg{
		UserID:      userID,
		DstBucketID: req.DstBucketID,
		DstDir:      req.DstDir,
		Items:       req.Items,
	})
	respondResult(c, results, nil)
}

// BatchMove 批量移动(POST /buckets/:id/items/batch-move)。
// 请求体:{"dst_bucket_id"?,"dst_dir"?,"items":[{"kind","id"}...]};缺省同桶。
func (FileAPI) BatchMove(c *gin.Context) {
	var req struct {
		bucketPathReq
		DstBucketID uint              `json:"dst_bucket_id"`
		DstDir      string            `json:"dst_dir"`
		Items       []server.MoveItem `json:"items"`
	}
	if !bindRequest(c, &req) {
		return
	}
	srcBucketID := req.BucketID
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}
	if err := server.CheckBatchItems(c.Request.Context(), server.CheckBatchItemsArg{Count: len(req.Items)}); err != nil {
		respondError(c, err)
		return
	}
	dstBucketID := req.DstBucketID
	if dstBucketID == 0 {
		dstBucketID = srcBucketID // 缺省同桶
	}
	userID := currentUser(c)

	// 源条目可读 + 目标桶可写
	failed := precheckItems(c, userID, srcBucketID,
		func(i int) (string, uint) { return req.Items[i].Kind, req.Items[i].SrcID }, len(req.Items))
	if len(failed) > 0 {
		respondResult(c, nil, failed)
		return
	}
	if _, err := precheckBucketWrite(c.Request.Context(), userID, dstBucketID); err != nil {
		respondError(c, err)
		return
	}

	results := server.MoveItems(c.Request.Context(), server.MoveItemsArg{
		UserID:      userID,
		DstBucketID: dstBucketID,
		DstDir:      req.DstDir,
		Items:       req.Items,
	})
	respondResult(c, results, nil)
}

// ---- 鉴权预检辅助 ----

// precheckItems 批量条目可见性预检(文件/文件夹存在性 + 条目级 + 祖先链);
// 任一条目失败即进 failed 列表并返回。
func precheckItems(c *gin.Context, userID, bucketID uint, kindID func(i int) (kind string, id uint), n int) []gin.H {
	failed := []gin.H{}
	for i := 0; i < n; i++ {
		kind, id := kindID(i)
		if err := precheckItem(c, userID, bucketID, kind, id); err != nil {
			failed = append(failed, gin.H{"kind": kind, "id": id, "error": err.Error()})
		}
	}
	return failed
}

// precheckItem 单条目鉴权预检:存在性 + 桶权限 + 条目级/祖先链可见性。
func precheckItem(c *gin.Context, userID, bucketID uint, kind string, id uint) error {
	if id == 0 {
		return server.ErrInvalidInput
	}
	switch kind {
	case server.ItemKindFile:
		_, err := precheckFileRead(c.Request.Context(), userID, bucketID, id)
		return err
	case server.ItemKindFolder:
		_, err := precheckFolderRead(c.Request.Context(), userID, bucketID, id)
		return err
	default:
		return server.ErrInvalidInput
	}
}

// respondResult 输出批量操作结果(成功项 / 失败项,HTTP 恒 200)。
func respondResult(c *gin.Context, results []server.BatchResultItem, failed []gin.H) {
	success := []gin.H{}
	for _, r := range results {
		if r.Error == "" {
			success = append(success, gin.H{"kind": r.Kind, "id": r.ID, "name": r.Name})
			continue
		}
		failed = append(failed, gin.H{"kind": r.Kind, "id": r.ID, "error": r.Error})
	}
	if failed == nil {
		failed = []gin.H{}
	}
	c.JSON(http.StatusOK, gin.H{"code": 0, "message": "ok", "data": gin.H{"success": success, "failed": failed}})
}
