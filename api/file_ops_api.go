// file_ops_api.go 文件模块操作接口:删除 / 复制 / 剪切 / 建目录 / 可见组设置
// (上传/列表/元数据/下载/预览见 file_api.go)。
package api

import (
	"context"

	agilepool "github.com/Yiming1997/agilePool/v2"
	"github.com/gin-gonic/gin"

	"orbitcloud/common"
	"orbitcloud/core"
	"orbitcloud/server"
)

// DeleteFile 删除文件(DELETE /buckets/:id/files/:fid,成功 204)。
func (FileAPI) DeleteFile(c *gin.Context) {
	bucketID := parseIDParam(c, "id")
	if bucketID == 0 {
		return
	}
	fileID := parseIDParam(c, "fid")
	if fileID == 0 {
		return
	}
	ctx := c.Request.Context()
	userID := currentUser(c)

	// 条目存在性 + 桶权限 + 条目/祖先 ACL
	if _, err := precheckFileRead(ctx, userID, bucketID, fileID); err != nil {
		respondError(c, err)
		return
	}

	if err := server.DeleteFile(ctx, server.DeleteFileArg{UserID: userID, BucketID: bucketID, FileID: fileID}); err != nil {
		respondError(c, err)
		return
	}

	c.Status(204)
}

// CopyFile 复制文件到目标位置(POST /buckets/:id/files/:fid/copy,仅文件)。
// 请求体:{"dst_bucket_id","dst_dir"?,"filename"?}(缺省:桶根目录、源文件名);
// 返回新文件记录。
func (FileAPI) CopyFile(c *gin.Context) {
	srcBucketID := parseIDParam(c, "id")
	if srcBucketID == 0 {
		return
	}
	srcFileID := parseIDParam(c, "fid")
	if srcFileID == 0 {
		return
	}

	var req struct {
		DstBucketID uint   `json:"dst_bucket_id"`
		DstDir      string `json:"dst_dir"`
		Filename    string `json:"filename"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}
	if req.DstBucketID == 0 {
		respondError(c, server.ErrInvalidInput)
		return
	}

	// 源端可读 + 目标桶可写
	ctx := c.Request.Context()
	userID := currentUser(c)
	if _, err := precheckFileRead(ctx, userID, srcBucketID, srcFileID); err != nil {
		respondError(c, err)
		return
	}
	if _, err := precheckBucketWrite(ctx, userID, req.DstBucketID); err != nil {
		respondError(c, err)
		return
	}

	f, err := server.CopyFile(ctx, server.CopyFileArg{UserID: userID, SrcBucketID: srcBucketID, SrcFileID: srcFileID, DstBucketID: req.DstBucketID, DstDirPath: req.DstDir, DstFilename: req.Filename})
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, f)
}

// CopyFolder 复制文件夹(POST /buckets/:id/dirs/:fid/copy)。
// 请求体:{"dst_bucket_id","dst_dir"?,"dst_name"?}(缺省:桶根目录、源目录名);
// 返回目标侧预建的顶层目录,子树由后台 CopyTask 完成。
func (FileAPI) CopyFolder(c *gin.Context) {
	srcBucketID := parseIDParam(c, "id")
	if srcBucketID == 0 {
		return
	}
	srcFolderID := parseIDParam(c, "fid")
	if srcFolderID == 0 {
		return
	}

	var req struct {
		DstBucketID uint   `json:"dst_bucket_id"`
		DstDir      string `json:"dst_dir"`
		DstName     string `json:"dst_name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}
	if req.DstBucketID == 0 {
		respondError(c, server.ErrInvalidInput)
		return
	}

	// 源端可读 + 目标桶可写
	ctx := c.Request.Context()
	userID := currentUser(c)
	if _, err := precheckFolderRead(ctx, userID, srcBucketID, srcFolderID); err != nil {
		respondError(c, err)
		return
	}
	if _, err := precheckBucketWrite(ctx, userID, req.DstBucketID); err != nil {
		respondError(c, err)
		return
	}

	f, err := server.CopyFolder(ctx, server.CopyFolderArg{UserID: userID, SrcBucketID: srcBucketID, SrcFolderID: srcFolderID, DstBucketID: req.DstBucketID, DstDirPath: req.DstDir, DstName: req.DstName})
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, f)
}

// MoveFile 移动/重命名文件(POST /buckets/:id/files/:fid/move)。
// 请求体:{"dst_bucket_id"?,"dst_dir"?,"filename"?}(缺省:同桶、桶根、原名)。
func (FileAPI) MoveFile(c *gin.Context) {
	srcBucketID := parseIDParam(c, "id")
	if srcBucketID == 0 {
		return
	}
	srcFileID := parseIDParam(c, "fid")
	if srcFileID == 0 {
		return
	}

	// 指针字段用于判断是否显式给目标桶
	var req struct {
		DstBucketID *uint  `json:"dst_bucket_id"`
		DstDir      string `json:"dst_dir"`
		Filename    string `json:"filename"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}
	dstBucketID := srcBucketID // 缺省同桶
	if req.DstBucketID != nil {
		dstBucketID = *req.DstBucketID
	}
	if dstBucketID == 0 {
		respondError(c, server.ErrInvalidInput)
		return
	}

	// 源端可读 + 目标桶可写
	ctx := c.Request.Context()
	userID := currentUser(c)
	if _, err := precheckFileRead(ctx, userID, srcBucketID, srcFileID); err != nil {
		respondError(c, err)
		return
	}
	if _, err := precheckBucketWrite(ctx, userID, dstBucketID); err != nil {
		respondError(c, err)
		return
	}

	f, err := server.MoveFile(ctx, server.MoveFileArg{UserID: userID, SrcBucketID: srcBucketID, SrcFileID: srcFileID, DstBucketID: dstBucketID, DstDirPath: req.DstDir, DstName: req.Filename})
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, f)
}

// MoveFolder 移动/重命名文件夹(POST /buckets/:id/dirs/:fid/move)。
// 同桶为单事务 O(1) 移动,子树零改动;跨桶不支持。
// 请求体:{"dst_bucket_id"?,"dst_dir"?,"filename"?}(缺省:同桶、桶根、原名)。
func (FileAPI) MoveFolder(c *gin.Context) {
	srcBucketID := parseIDParam(c, "id")
	if srcBucketID == 0 {
		return
	}
	srcDirID := parseIDParam(c, "fid")
	if srcDirID == 0 {
		return
	}

	// 指针字段用于判断是否显式给目标桶
	var req struct {
		DstBucketID *uint  `json:"dst_bucket_id"`
		DstDir      string `json:"dst_dir"`
		Filename    string `json:"filename"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}
	dstBucketID := srcBucketID // 缺省同桶
	if req.DstBucketID != nil {
		dstBucketID = *req.DstBucketID
	}
	if dstBucketID == 0 {
		respondError(c, server.ErrInvalidInput)
		return
	}

	// 源端可读 + 目标桶可写
	ctx := c.Request.Context()
	userID := currentUser(c)
	if _, err := precheckFolderRead(ctx, userID, srcBucketID, srcDirID); err != nil {
		respondError(c, err)
		return
	}
	if _, err := precheckBucketWrite(ctx, userID, dstBucketID); err != nil {
		respondError(c, err)
		return
	}

	f, err := server.MoveFolder(ctx, server.MoveFolderArg{UserID: userID, SrcBucketID: srcBucketID, SrcFolderID: srcDirID, DstBucketID: dstBucketID, DstDirPath: req.DstDir, DstName: req.Filename})
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, f)
}

// CreateDir 创建目录(POST /buckets/:id/dirs,mkdir -p)。
// 请求体:{"path": "dir/sub"};父链自动创建,已存在同名文件夹幂等成功。
func (FileAPI) CreateDir(c *gin.Context) {
	bucketID := parseIDParam(c, "id")
	if bucketID == 0 {
		return
	}

	var req struct {
		Path string `json:"path"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}

	// 桶可写(目标目录祖先链 ACL 由 server 内部校验)
	ctx := c.Request.Context()
	userID := currentUser(c)
	if _, err := precheckBucketWrite(ctx, userID, bucketID); err != nil {
		respondError(c, err)
		return
	}

	item, err := server.CreateDir(ctx, server.CreateDirArg{UserID: userID, BucketID: bucketID, DirPath: req.Path})
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, item)
}

// DeleteDir 删除文件夹(DELETE /buckets/:id/dirs/:fid,成功 204)。
// 内部置为不可见并落删除任务,目录立即从列表消失;
// 物理清理经全局协程池后台执行(背压:池满时提交阻塞,请求自然排队)。
func (FileAPI) DeleteDir(c *gin.Context) {
	bucketID := parseIDParam(c, "id")
	if bucketID == 0 {
		return
	}
	dirID := parseIDParam(c, "fid")
	if dirID == 0 {
		return
	}

	// 条目存在性 + 桶权限 + 条目/祖先 ACL
	userID := currentUser(c)
	if _, err := precheckFolderRead(c.Request.Context(), userID, bucketID, dirID); err != nil {
		respondError(c, err)
		return
	}

	// 落删除任务(幂等,中断由启动/cron 续跑)
	taskID, err := server.DeleteDir(c.Request.Context(), server.DeleteDirArg{UserID: userID, BucketID: bucketID, DirID: dirID})
	if err != nil {
		respondError(c, err)
		return
	}

	// 经全局协程池提交物理清理:SubmitCtx 传入 gin 请求上下文——
	// 客户端断开后不再提交/未开始的任务被跳过(任务留在任务表由 cron 续跑);
	// 任务已开始执行则内部 context.WithoutCancel 继续,不随断开中断
	core.Pool.SubmitCtx(c, agilepool.TaskFunc(func() error {
		return server.ProcessDeleteTask(context.Background(), server.ProcessDeleteTaskArg{TaskID: taskID})
	}))

	c.Status(204)
}

// SetFileVisibility 设置文件可见组(PUT /buckets/:id/files/:fid/visibility)。
// 请求体:{"groups":[组ID...]}(空数组 = 恢复不限制);需创建者或管理员。
func (FileAPI) SetFileVisibility(c *gin.Context) {
	bucketID := parseIDParam(c, "id")
	if bucketID == 0 {
		return
	}
	fileID := parseIDParam(c, "fid")
	if fileID == 0 {
		return
	}
	ctx := c.Request.Context()
	userID := currentUser(c)

	// 条目存在 → 桶权限 + 归属(创建者或管理员)
	file, err := precheckFileRead(ctx, userID, bucketID, fileID)
	if err != nil {
		respondError(c, err)
		return
	}
	if err := permOwnerOrAdmin(ctx, userID, file.UploadedBy); err != nil {
		respondError(c, err)
		return
	}

	var req struct {
		Groups []uint `json:"groups"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}
	if err := server.SetFileVisibility(ctx, server.SetFileVisibilityArg{UserID: userID, BucketID: bucketID, FileID: fileID, GroupIDs: req.Groups}); err != nil {
		respondError(c, err)
		return
	}
	common.Success(c, gin.H{"file_id": fileID, "visible_to_groups": req.Groups})
}

// SetFolderVisibility 设置文件夹可见组(PUT /buckets/:id/dirs/:fid/visibility)。
// 请求体:{"groups":[组ID...]}(空数组 = 恢复不限制);需创建者或管理员;
// 仅影响本目录行,不递归子树。
func (FileAPI) SetFolderVisibility(c *gin.Context) {
	bucketID := parseIDParam(c, "id")
	if bucketID == 0 {
		return
	}
	dirID := parseIDParam(c, "fid")
	if dirID == 0 {
		return
	}
	ctx := c.Request.Context()
	userID := currentUser(c)

	// 条目存在 → 桶权限 + 归属(创建者或管理员)
	folder, err := precheckFolderRead(ctx, userID, bucketID, dirID)
	if err != nil {
		respondError(c, err)
		return
	}
	if err := permOwnerOrAdmin(ctx, userID, folder.UploadedBy); err != nil {
		respondError(c, err)
		return
	}

	var req struct {
		Groups []uint `json:"groups"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}
	if err := server.SetFolderVisibility(ctx, server.SetFolderVisibilityArg{UserID: userID, BucketID: bucketID, FolderID: dirID, GroupIDs: req.Groups}); err != nil {
		respondError(c, err)
		return
	}
	common.Success(c, gin.H{"folder_id": dirID, "visible_to_groups": req.Groups})
}
