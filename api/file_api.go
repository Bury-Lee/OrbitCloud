// file_api.go 文件模块接口:上传 / 列表 / 元数据 / 下载 / 预览(增删改等见 file_ops_api.go)。
// 路由按类型拆分:文件走 /files/:fid,文件夹走 /dirs/:fid,消除同号寻址二义。
package api

import (
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"orbitcloud/common"
	"orbitcloud/model"
	"orbitcloud/server"
	"orbitcloud/utils"
)

// FileAPI 文件模块接口(无状态;全部路由需 AuthMiddleware)。
type FileAPI struct{}

// parseIDParam 解析 gin 路径参数 :name 为 uint;非法时写出 400 并返回 0(调用方应提前 return)。
func parseIDParam(c *gin.Context, name string) uint {
	v := c.Param(name)
	id, err := strconv.ParseUint(v, 10, 64)
	if err != nil || id == 0 {
		respondError(c, server.ErrInvalidInput)
		c.Abort()
		return 0
	}
	return uint(id)
}

// UploadFile 上传单文件(POST /buckets/:id/files?path=dir/sub,multipart 字段 file)。
// path 为目标目录(缺省桶根 "/"),父目录链自动创建;返回 model.File。
func (FileAPI) UploadFile(c *gin.Context) {
	bucketID := parseIDParam(c, "id")
	if bucketID == 0 {
		return
	}
	ctx := c.Request.Context()
	userID := currentUser(c)

	// 桶可写(目标目录祖先链 ACL 由 server 内部校验)
	if _, err := precheckBucketWrite(ctx, userID, bucketID); err != nil {
		respondError(c, err)
		return
	}

	// 目标目录(query path,缺省桶根 "/")
	dirPath := c.Query("path")

	// 流式解析 multipart,不整读内存(与 server.UploadFile 的 io.Reader 契约配合)
	fh, err := c.FormFile("file")
	if err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}
	file, err := fh.Open()
	if err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}
	defer file.Close()

	f, err := server.UploadFile(ctx, server.UploadFileArg{UserID: userID, BucketID: bucketID, DirPath: dirPath, Filename: fh.Filename, Reader: file})
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, f)
}

// UploadFiles 批量上传(POST /buckets/:id/files/batch?path=&folder_id=,multipart 字段 files)。
// 目标目录可用 query path 或 folder_id 直传(缺省桶根 "/");逐文件独立处理,
// 部分失败不影响其它文件,返回 {success, failed}。
func (FileAPI) UploadFiles(c *gin.Context) {
	bucketID := parseIDParam(c, "id")
	if bucketID == 0 {
		return
	}
	userID := currentUser(c)
	ctx := c.Request.Context()

	// 桶可写
	if _, err := precheckBucketWrite(ctx, userID, bucketID); err != nil {
		respondError(c, err)
		return
	}

	// 目标目录(query path 或 folder_id 直传,缺省桶根 "/")
	dirPath := c.Query("path")
	folderID := uint(0)
	if fid := queryInt(c, "folder_id", 0); fid > 0 {
		folderID = uint(fid)
	}

	// 超内存上限的部分由框架落临时文件
	form, err := c.MultipartForm()
	if err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}
	fhs := form.File["files"]
	if len(fhs) == 0 {
		respondError(c, server.ErrInvalidInput)
		return
	}

	// 组装批量参数并调 server.UploadFiles(逐文件独立处理)
	items := make([]server.UploadItem, 0, len(fhs))
	for _, fh := range fhs {
		f, err := fh.Open()
		if err != nil {
			continue
		}
		items = append(items, server.UploadItem{Name: fh.Filename, Reader: f})
	}
	results := server.UploadFiles(ctx, server.UploadFilesArg{
		UserID:   userID,
		BucketID: bucketID,
		DirPath:  dirPath,
		FolderID: folderID,
		Items:    items,
	})

	// 组装响应(成功对象 / 失败项逐条;Reader 已由 server 消费完毕,统一关闭)
	success := make([]*model.File, 0, len(results))
	failed := make([]gin.H, 0)
	for _, r := range results {
		if r.Error == "" {
			if f, ok := r.Data.(*model.File); ok {
				success = append(success, f)
			}
			continue
		}
		failed = append(failed, gin.H{"name": r.Name, "error": r.Error})
	}
	for _, it := range items {
		if closer, ok := it.Reader.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}

	common.Success(c, gin.H{"success": success, "failed": failed})
}

// ListFiles 分页列出目录内容(GET /buckets/:id/files?path=&page=&page_size=),
// 返回 {files, folders, total} 两个类型化切片。
// cursor=1 时为游标模式:双列表分别 keyset 分页(不返回 total),供前端逐层下载。
func (FileAPI) ListFiles(c *gin.Context) {
	bucketID := parseIDParam(c, "id")
	if bucketID == 0 {
		return
	}
	ctx := c.Request.Context()
	userID := currentUser(c)

	// 桶可读(目标目录祖先链 ACL 由 server 内部校验)
	if _, err := precheckBucketRead(ctx, userID, bucketID); err != nil {
		respondError(c, err)
		return
	}

	dirPath := c.Query("path")

	// 游标模式(前端递归下载):双列表分别 keyset 分页
	if c.Query("cursor") == "1" {
		pageSize := queryInt(c, "page_size", 50)
		files, folders, nextFileCursor, nextFolderCursor, err := server.ListFilesCursor(ctx, server.ListFilesCursorArg{UserID: userID, BucketID: bucketID, DirPath: dirPath, FileCursor: c.Query("files_cursor"), FolderCursor: c.Query("folders_cursor"), PageSize: pageSize})
		if err != nil {
			respondError(c, err)
			return
		}
		common.Success(c, gin.H{
			"files":               files,
			"folders":             folders,
			"next_files_cursor":   nextFileCursor,
			"next_folders_cursor": nextFolderCursor,
		})
		return
	}

	// offset 模式(页面浏览):分页参数(默认 1 / 50)
	page := queryInt(c, "page", 1)
	pageSize := queryInt(c, "page_size", 50)

	files, folders, total, err := server.ListFiles(ctx, server.ListFilesArg{UserID: userID, BucketID: bucketID, DirPath: dirPath, Page: page, PageSize: pageSize})
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, gin.H{"files": files, "folders": folders, "total": total})
}

// GetFileMeta 返回文件元数据(GET /buckets/:id/files/:fid,仅文件)。
func (FileAPI) GetFileMeta(c *gin.Context) {
	bucketID := parseIDParam(c, "id")
	if bucketID == 0 {
		return
	}
	fileID := parseIDParam(c, "fid")
	if fileID == 0 {
		return
	}

	meta, err := precheckFileRead(c.Request.Context(), currentUser(c), bucketID, fileID)
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, meta)
}

// GetFolderMeta 返回文件夹元数据(GET /buckets/:id/dirs/:fid,仅文件夹)。
func (FileAPI) GetFolderMeta(c *gin.Context) {
	bucketID := parseIDParam(c, "id")
	if bucketID == 0 {
		return
	}
	dirID := parseIDParam(c, "fid")
	if dirID == 0 {
		return
	}

	meta, err := precheckFolderRead(c.Request.Context(), currentUser(c), bucketID, dirID)
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, meta)
}

// DownloadFile 下载文件(GET /buckets/:id/files/:fid/download)。
// 支持 HTTP Range 头(单区间):无 Range → 200 全量;合法区间 → 206;非法/越界 → 416。
func (FileAPI) DownloadFile(c *gin.Context) {
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

	// 权限预检返回的文件元数据含 FileSize,直接供 Range 解析
	file, err := precheckFileRead(ctx, userID, bucketID, fileID)
	if err != nil {
		respondError(c, err)
		return
	}

	// Range 预处理:非法/越界立即 416,不触碰对象存储
	br, err := parseRangeHeader(c, file)
	if err != nil {
		return
	}

	// 按有无 Range 选下载通路(全量整流 / 区间流)
	var rc io.ReadCloser
	var meta *model.File
	if br == nil {
		rc, meta, err = server.DownloadFile(ctx, server.DownloadFileArg{BucketID: bucketID, FileID: fileID})
	} else {
		rc, meta, err = server.DownloadFileRange(ctx, server.DownloadFileRangeArg{BucketID: bucketID, FileID: fileID, Start: br.Start, End: br.End})
	}
	if err != nil {
		respondError(c, err)
		return
	}

	serveFileStream(c, rc, meta,
		"attachment; filename*=UTF-8''"+url.QueryEscape(meta.Name), "application/octet-stream", br)
}

// PreviewFile 浏览器内预览文件(GET /buckets/:id/files/:fid/preview,inline)。
// 内容类型经白名单判定:可执行文本降级 text/plain 只读,其余二进制降级附件;
// 附安全头;支持 Range。
func (FileAPI) PreviewFile(c *gin.Context) {
	bucketID := parseIDParam(c, "id")
	if bucketID == 0 {
		return
	}
	fileID := parseIDParam(c, "fid")
	if fileID == 0 {
		return
	}

	writePreviewStream(c, bucketID, fileID)
}

// StreamFile 音视频流式播放(GET /buckets/:id/files/:fid/stream)。
// 与 PreviewFile 同通路、同响应头语义;鉴权走 ?token= 查询参数
// (HTML 媒体元素发起的 Range 请求无法携带 Authorization 头)。
func (FileAPI) StreamFile(c *gin.Context) {
	bucketID := parseIDParam(c, "id")
	if bucketID == 0 {
		return
	}
	fileID := parseIDParam(c, "fid")
	if fileID == 0 {
		return
	}

	writePreviewStream(c, bucketID, fileID)
}

// writePreviewStream 预览/流媒体共用通路:权限预检 + Range 预处理 + 类型判定 + 安全头 + 流写出。
func writePreviewStream(c *gin.Context, bucketID, fileID uint) {
	ctx := c.Request.Context()
	userID := currentUser(c)

	file, err := precheckFileRead(ctx, userID, bucketID, fileID)
	if err != nil {
		respondError(c, err)
		return
	}

	br, err := parseRangeHeader(c, file)
	if err != nil {
		return
	}

	var rc io.ReadCloser
	var meta *model.File
	if br == nil {
		rc, meta, err = server.DownloadFile(ctx, server.DownloadFileArg{BucketID: bucketID, FileID: fileID})
	} else {
		rc, meta, err = server.DownloadFileRange(ctx, server.DownloadFileRangeArg{BucketID: bucketID, FileID: fileID, Start: br.Start, End: br.End})
	}
	if err != nil {
		respondError(c, err)
		return
	}

	contentType, disposition := previewContentType(meta)
	// nosniff:禁止浏览器内容嗅探执行
	c.Header("X-Content-Type-Options", "nosniff")
	// sandbox:内联内容在沙箱中渲染,隔离脚本/表单/顶层导航
	c.Header("Content-Security-Policy", "sandbox")
	serveFileStream(c, rc, meta, disposition, contentType, br)
}

// previewContentType 按内容类型白名单判定预览响应 (Content-Type, Content-Disposition):
// 安全类型(图片/音视频/PDF/纯文本等)原类型 inline;可执行文本类(html/svg/xml/
// javascript/css)强制 text/plain 只读;其余二进制降级为附件下载。
func previewContentType(meta *model.File) (string, string) {
	t := strings.ToLower(strings.TrimSpace(meta.FileType))
	inline := "inline"

	// 白名单安全类别(无脚本执行能力):原类型 inline
	switch {
	case t == "application/pdf":
		return t, inline
	case t == "application/json":
		return t, inline
	case strings.HasPrefix(t, "image/"):
		if t != "image/svg+xml" { // SVG 可内嵌脚本 → 降级(见下)
			return t, inline
		}
	case strings.HasPrefix(t, "audio/"), strings.HasPrefix(t, "video/"):
		return t, inline
	case t == "text/plain" || t == "text/markdown" || t == "text/csv":
		return t, inline
	}

	// 可执行文本类(有脚本执行能力):仅读源码,禁止执行 → 强制 text/plain
	switch {
	case strings.HasPrefix(t, "text/"): // text/html、text/css、text/javascript 等全部降级
		return "text/plain; charset=utf-8", inline
	case t == "image/svg+xml" || t == "application/xhtml+xml" ||
		t == "application/xml" || t == "text/xml" ||
		strings.Contains(t, "javascript"):
		return "text/plain; charset=utf-8", inline
	}

	// 其余(二进制/未知):不可安全渲染,降级为附件下载
	return "application/octet-stream", "attachment; filename*=UTF-8''" + url.QueryEscape(meta.Name)
}

// parseRangeHeader 解析 Range 头(file 为已通过权限预检的文件元数据):
// 无 Range 头 → 返回 (nil, nil)(按 200 全量处理);非法/越界 → 写出 416 并返回错误。
func parseRangeHeader(c *gin.Context, file *model.File) (*utils.ByteRange, error) {
	rangeHeader := c.GetHeader("Range")
	if rangeHeader == "" {
		return nil, nil // 无 Range:调用方按 200 全量处理
	}
	br, err := utils.ParseRange(rangeHeader, file.FileSize)
	if err != nil {
		writeRangeNotSatisfiable(c, file.FileSize)
		return nil, err
	}
	return br, nil
}
