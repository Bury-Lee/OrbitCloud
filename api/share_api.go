// share_api.go 分享模块接口:创建 / 列表 / 修改 / 删除 / 公开解析下载。
// 分享仅只读;解析元数据不计数,实际下载/预览才计数;上传者或管理员可创建分享。
package api

import (
	"io"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"

	"orbitcloud/common"
	"orbitcloud/model"
	"orbitcloud/server"
	"orbitcloud/utils"
)

// ShareAPI 分享模块接口(无状态):解析/下载公开,创建/列表/修改/删除需 AuthMiddleware。
type ShareAPI struct{}

// CreateShare 创建分享(POST /shares)。
// 请求体:{"file_id","permission"?,"expires_at"?,"max_downloads"?,"password"?};
// 权限仅支持 read;需为上传者或管理员。
func (ShareAPI) CreateShare(c *gin.Context) {
	var req struct {
		FileID       uint       `json:"file_id"`
		Permission   string     `json:"permission"`
		ExpiresAt    *time.Time `json:"expires_at"`
		MaxDownloads int        `json:"max_downloads"`
		Password     string     `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}
	if req.FileID == 0 {
		respondError(c, server.ErrInvalidInput)
		return
	}
	ctx := c.Request.Context()
	userID := currentUser(c)

	// 归属校验:先按文件夹定位,再按文件定位,与 server.CreateShare 的分派一致
	ownerID := uint(0)
	if folder, err := server.LoadFolderByID(ctx, server.LoadFolderByIDArg{FolderID: req.FileID}); err == nil {
		ownerID = folder.UploadedBy
	} else if file, err := server.LoadFileByID(ctx, server.LoadFileByIDArg{FileID: req.FileID}); err == nil {
		ownerID = file.UploadedBy
	} else {
		respondError(c, server.ErrNotFound)
		return
	}
	if err := permOwnerOrAdmin(ctx, userID, ownerID); err != nil {
		respondError(c, err)
		return
	}

	// Permission 仅支持 read(edit 不开放,server 层归一为 read)
	arg := server.CreateShareArg{
		UserID:       userID,
		FileID:       req.FileID,
		Permission:   req.Permission,
		MaxDownloads: req.MaxDownloads,
		Password:     req.Password,
	}
	if req.ExpiresAt != nil {
		arg.ExpiresAt = *req.ExpiresAt
	}

	share, err := server.CreateShare(ctx, arg)
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, share)
}

// ListShares 分页返回本人创建的分享(GET /shares?page=&page_size=)。
func (ShareAPI) ListShares(c *gin.Context) {
	page := queryInt(c, "page", 1)
	pageSize := queryInt(c, "page_size", 50)

	total, items, err := server.ListShares(c.Request.Context(), server.ListSharesArg{UserID: currentUser(c), Page: page, PageSize: pageSize})
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, gin.H{"total": total, "items": items})
}

// UpdateShare 更新分享(PUT /shares/:id,仅创建者)。
// 请求体:{"permission"?,"expires_at"?,"max_downloads"?,"password"?}(指针字段才更新)。
func (ShareAPI) UpdateShare(c *gin.Context) {
	shareID := parseIDParam(c, "id")
	if shareID == 0 {
		return
	}
	ctx := c.Request.Context()
	userID := currentUser(c)

	// 分享存在 → 归属校验(仅创建者)
	share, err := server.LoadShare(ctx, server.LoadShareArg{ShareID: shareID})
	if err != nil {
		respondError(c, err)
		return
	}
	if share.CreatorID != userID {
		respondError(c, server.ErrForbidden)
		return
	}

	var req struct {
		Permission   *string    `json:"permission"`
		ExpiresAt    *time.Time `json:"expires_at"`
		MaxDownloads *int       `json:"max_downloads"`
		Password     *string    `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, server.ErrInvalidInput)
		return
	}

	// 指针字段原样透传,缺失字段留 nil
	in := server.UpdateShareInput{
		Permission:   req.Permission,
		ExpiresAt:    req.ExpiresAt,
		MaxDownloads: req.MaxDownloads,
		Password:     req.Password,
	}

	updated, err := server.UpdateShare(ctx, server.UpdateShareArg{ShareID: shareID, In: in})
	if err != nil {
		respondError(c, err)
		return
	}

	common.Success(c, updated)
}

// DeleteShare 删除分享(DELETE /shares/:id,成功 204);仅创建者可删。
func (ShareAPI) DeleteShare(c *gin.Context) {
	shareID := parseIDParam(c, "id")
	if shareID == 0 {
		return
	}
	ctx := c.Request.Context()
	userID := currentUser(c)

	// 分享存在 → 归属校验(仅创建者)
	share, err := server.LoadShare(ctx, server.LoadShareArg{ShareID: shareID})
	if err != nil {
		respondError(c, err)
		return
	}
	if share.CreatorID != userID {
		respondError(c, server.ErrForbidden)
		return
	}

	if err := server.DeleteShare(ctx, server.DeleteShareArg{ShareID: shareID}); err != nil {
		respondError(c, err)
		return
	}

	c.Status(204)
}

// ResolveShare 公开解析分享(GET /share/:token?password=&download=1)。
// download=1(或 preview=1)时直出文件流(带提取码/次数校验,支持 Range,计次数);
// 否则返回分享元数据(文件分享返回 model.File,文件夹分享返回 model.Folder + 目录列表,不计数)。
func (ShareAPI) ResolveShare(c *gin.Context) {
	token := c.Param("token")
	if token == "" {
		respondError(c, server.ErrInvalidInput)
		return
	}
	password := c.Query("password") // 无则空串
	path := c.Query("path")         // 文件夹分享:分享根内相对路径
	download := c.Query("download") == "1" || c.Query("preview") == "1"

	// 直出文件流(文件夹分享经 path 定位下载目录内文件)
	if download {
		ctx := c.Request.Context()

		// Range 预处理:有 Range 头时先取文件大小(不计数)并解析区间,非法/越界立即 416
		rangeHeader := c.GetHeader("Range")
		var br *utils.ByteRange
		if rangeHeader != "" {
			meta0, err := server.SharedFileMeta(ctx, server.SharedFileMetaArg{Token: token, Password: password, RelPath: path})
			if err != nil {
				respondError(c, err)
				return
			}
			br, err = utils.ParseRange(rangeHeader, meta0.FileSize)
			if err != nil {
				writeRangeNotSatisfiable(c, meta0.FileSize)
				return
			}
		}

		// 按有无 Range 选下载通路(全量整流 / 区间流)
		var rc io.ReadCloser
		var meta *model.File
		var err error
		if br == nil {
			rc, meta, err = server.DownloadSharedFile(ctx, server.DownloadSharedFileArg{Token: token, Password: password, RelPath: path})
		} else {
			rc, meta, err = server.DownloadSharedFileRange(ctx, server.DownloadSharedFileRangeArg{Token: token, Password: password, RelPath: path, Start: br.Start, End: br.End})
		}
		if err != nil {
			respondError(c, err)
			return
		}

		serveFileStream(c, rc, meta,
			"attachment; filename*=UTF-8''"+url.QueryEscape(meta.Name), meta.FileType, br)
		return
	}

	// 仅解析元数据(不计数;统一 404 防探测由 server 层处理)
	file, folder, err := server.ResolveShare(c.Request.Context(), server.ResolveShareArg{Token: token, Password: password})
	if err != nil {
		respondError(c, err)
		return
	}
	if folder != nil {
		// 文件夹分享:附目录列表
		page := queryInt(c, "page", 1)
		pageSize := queryInt(c, "page_size", 50)
		files, folders, total, err := server.ListSharedDir(c.Request.Context(), server.ListSharedDirArg{Token: token, Password: password, RelPath: path, Page: page, PageSize: pageSize})
		if err != nil {
			respondError(c, err)
			return
		}
		common.Success(c, gin.H{"meta": folder, "files": files, "folders": folders, "total": total})
		return
	}
	common.Success(c, file)
}
