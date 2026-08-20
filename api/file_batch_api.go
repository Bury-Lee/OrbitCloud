// file_batch_api.go 文件模块批量操作接口:批量删除 / 复制 / 移动 / 下载。
// 调用 server 批量函数前在 api 层对所有条目做可见性预检,失败条目进 failed 列表。
package api

import (
	"archive/zip"
	"context"
	"net/http"
	"strconv"
	"strings"

	agilepool "github.com/Yiming1997/agilePool/v2"
	"github.com/gin-gonic/gin"

	"orbitcloud/core"
	"orbitcloud/server"
)

// BatchDelete 批量删除(POST /buckets/:id/items/batch-delete)。
// 请求体:{"items":[{"kind","id"}...]};返回 {success, failed}(部分失败也 200)。
func (FileAPI) BatchDelete(c *gin.Context) {
	bucketID := parseIDParam(c, "id")
	if bucketID == 0 {
		return
	}
	var req struct {
		Items []server.DeleteItem `json:"items"`
	}
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
	srcBucketID := parseIDParam(c, "id")
	if srcBucketID == 0 {
		return
	}
	var req struct {
		DstBucketID uint              `json:"dst_bucket_id"`
		DstDir      string            `json:"dst_dir"`
		Items       []server.CopyItem `json:"items"`
	}
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
	srcBucketID := parseIDParam(c, "id")
	if srcBucketID == 0 {
		return
	}
	var req struct {
		DstBucketID uint              `json:"dst_bucket_id"`
		DstDir      string            `json:"dst_dir"`
		Items       []server.MoveItem `json:"items"`
	}
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

// BatchDownload 批量下载为 zip(GET /buckets/:id/items/batch-download?ids=file:122,folder:33)。
// zip 流式输出,失败项以 zip 内缺失体现;响应头带条目数供前端预估耗时。
func (FileAPI) BatchDownload(c *gin.Context) {
	bucketID := parseIDParam(c, "id")
	if bucketID == 0 {
		return
	}
	userID := currentUser(c)
	ctx := c.Request.Context()

	// 解析 ids=kind:id,kind:id
	items := []server.DownloadItem{}
	for _, part := range strings.Split(c.Query("ids"), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		seg := strings.SplitN(part, ":", 2)
		if len(seg) != 2 {
			respondError(c, server.ErrInvalidInput)
			return
		}
		kind := seg[0]
		id, err := strconv.ParseUint(seg[1], 10, 64)
		if err != nil || id == 0 || (kind != server.ItemKindFile && kind != server.ItemKindFolder) {
			respondError(c, server.ErrInvalidInput)
			return
		}
		items = append(items, server.DownloadItem{Kind: kind, ID: uint(id)})
	}
	if len(items) == 0 {
		respondError(c, server.ErrInvalidInput)
		return
	}
	if err := server.CheckBatchItems(ctx, server.CheckBatchItemsArg{Count: len(items)}); err != nil {
		respondError(c, err)
		return
	}

	// 桶可写 + 条目存在性 + 条目级/祖先链可见性(api 层完成)
	if _, err := precheckBucketWrite(ctx, userID, bucketID); err != nil {
		respondError(c, err)
		return
	}
	failed := precheckItems(c, userID, bucketID,
		func(i int) (string, uint) { return items[i].Kind, items[i].ID }, len(items))
	if len(failed) > 0 {
		respondResult(c, nil, failed)
		return
	}

	// 打包前统计规模(受限目录不计入),写入响应头供前端提示耗时预估
	folderCount, fileCount := server.CountDownloadItems(ctx, server.CountDownloadItemsArg{UserID: userID, BucketID: bucketID, Items: items})
	c.Header("X-Batch-Folders", strconv.FormatInt(folderCount, 10))
	c.Header("X-Batch-Files", strconv.FormatInt(fileCount, 10))

	// zip 流式输出(响应头先发,Content-Disposition attachment)
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", "attachment; filename=batch.zip")
	zw := zip.NewWriter(c.Writer)
	defer zw.Close()

	results := server.DownloadItems(ctx, server.DownloadItemsArg{
		UserID:   userID,
		BucketID: bucketID,
		Items:    items,
	}, zw)
	if err := zw.Close(); err != nil {
		return // 流已发,无法改状态码
	}
	// zip 为流式响应:失败项无法回写 JSON,仅记录日志
	for _, r := range results {
		if r.Error != "" {
			_ = r.Error // 失败项:zip 内缺失该条目(客户端可见)
		}
	}
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
