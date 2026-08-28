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
	var req filePathReq
	if !bindRequest(c, &req) {
		return
	}
	bucketID, fileID := req.BucketID, req.FileID
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
	var req struct {
		filePathReq
		DstBucketID uint   `json:"dst_bucket_id"`
		DstDir      string `json:"dst_dir"`
		Filename    string `json:"filename"`
	}
	if !bindRequest(c, &req) {
		return
	}
	srcBucketID, srcFileID := req.BucketID, req.FileID
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
	var req struct {
		dirPathReq
		DstBucketID uint   `json:"dst_bucket_id"`
		DstDir      string `json:"dst_dir"`
		DstName     string `json:"dst_name"`
	}
	if !bindRequest(c, &req) {
		return
	}
	srcBucketID, srcFolderID := req.BucketID, req.DirID
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
	var req struct {
		filePathReq
		DstBucketID *uint  `json:"dst_bucket_id"`
		DstDir      string `json:"dst_dir"`
		Filename    string `json:"filename"`
	}
	if !bindRequest(c, &req) {
		return
	}
	srcBucketID, srcFileID := req.BucketID, req.FileID

	// 指针字段用于判断是否显式给目标桶
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
	var req struct {
		dirPathReq
		DstBucketID *uint  `json:"dst_bucket_id"`
		DstDir      string `json:"dst_dir"`
		Filename    string `json:"filename"`
	}
	if !bindRequest(c, &req) {
		return
	}
	srcBucketID, srcDirID := req.BucketID, req.DirID

	// 指针字段用于判断是否显式给目标桶
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
	var req struct {
		bucketPathReq
		Path string `json:"path"`
	}
	if !bindRequest(c, &req) {
		return
	}
	bucketID := req.BucketID
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
// 落删除任务后文件夹置"正在删除"(可见可读,拒写),后台先删内部(逐条软删进回收站)
// 再删文件夹自身,见 server.DeleteDir / processDeleteDirTask。
func (FileAPI) DeleteDir(c *gin.Context) {
	var req dirPathReq
	if !bindRequest(c, &req) {
		return
	}
	bucketID, dirID := req.BucketID, req.DirID

	// 条目存在性 + 桶权限 + 条目/祖先 ACL
	userID := currentUser(c)
	if _, err := precheckFolderRead(c.Request.Context(), userID, bucketID, dirID); err != nil {
		respondError(c, err)
		return
	}

	// 进回收站(幂等;taskID 恒 0 = 无需物理清理)
	taskID, err := server.DeleteDir(c.Request.Context(), server.DeleteDirArg{UserID: userID, BucketID: bucketID, DirID: dirID})
	if err != nil {
		respondError(c, err)
		return
	}

	// 防御:返回 taskID>0(如未来回收站清除复用该通道)才经全局协程池提交
	if taskID > 0 {
		core.Pool.SubmitCtx(c, agilepool.TaskFunc(func() error {
			return server.ProcessDeleteTask(context.Background(), server.ProcessDeleteTaskArg{TaskID: taskID})
		}))
	}

	c.Status(204)
}

// SetFileVisibility 设置文件可见组(PUT /buckets/:id/files/:fid/visibility)。
// 请求体:{"groups":[组ID...]}(空数组 = 恢复不限制);需创建者或管理员。
func (FileAPI) SetFileVisibility(c *gin.Context) {
	var req struct {
		filePathReq
		Groups []uint `json:"groups"`
	}
	if !bindRequest(c, &req) {
		return
	}
	bucketID, fileID := req.BucketID, req.FileID
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
	var req struct {
		dirPathReq
		Groups []uint `json:"groups"`
	}
	if !bindRequest(c, &req) {
		return
	}
	bucketID, dirID := req.BucketID, req.DirID
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
