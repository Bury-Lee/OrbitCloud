// share_access.go —— 分享链接访问层:校验 / 解析 / 下载 / 目录列表(创建与修改见 share.go)。
//
// 分享通道为公开访问(无登录身份),凭证即 token/提取码/有效期;
// 受限条目不构成分享限制(分享 = 显式授权通道,与站内 ACL 解耦);
// 下载次数自增属配额规则,保留在本层。
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"orbitcloud/common"
	"orbitcloud/core"
	"orbitcloud/log"
	"orbitcloud/model"
	"orbitcloud/utils"
)

// checkShare 分享通用校验(查分享/过期/提取码/条目解析),不涉及下载次数;
// 返回 (share, file, folder),文件/文件夹至多一个非 nil。
func checkShare(ctx context.Context, token, password string) (*model.ShareLink, *model.File, *model.Folder, error) {
	// 查分享
	share, err := GetShareByToken(ctx, GetShareByTokenArg{Token: token})
	if err != nil {
		return nil, nil, nil, err
	}

	// 过期校验(统一 404 防探测)
	if share.ExpiresAt != nil && time.Now().After(*share.ExpiresAt) {
		return nil, nil, nil, ErrNotFound
	}

	// 提取码校验
	if share.Password != "" {
		if err := bcrypt.CompareHashAndPassword([]byte(share.Password), []byte(password)); err != nil {
			return nil, nil, nil, ErrUnauthorized // 提取码错误
		}
	}

	// 按 ItemType 解析被分享条目(按主键查,已删 → ErrNotFound;受限状态不构成分享限制)
	if share.ItemType == ItemKindFolder {
		folder, err := loadFolderByID(ctx, share.BucketItemID)
		if err != nil {
			return nil, nil, nil, ErrNotFound
		}
		return share, nil, folder, nil
	}
	file, err := loadFileByID(ctx, share.BucketItemID)
	if err != nil {
		return nil, nil, nil, ErrNotFound
	}
	return share, file, nil, nil
}

// ResolveShareArg 分享解析入参。
type ResolveShareArg struct {
	Token    string // 分享短码
	Password string // 提取码明文(无则空串)
}

// ResolveShare 解析分享并返回被分享条目元数据(公开访问入口,不计数):
// 返回 (file, folder),调用方据此区分类型。
// 错误语义:分享不存在/已过期 → ErrNotFound;提取码错误 → ErrUnauthorized。
func ResolveShare(ctx context.Context, arg ResolveShareArg) (*model.File, *model.Folder, error) {
	token, password := arg.Token, arg.Password
	// 通用校验(不计数)
	_, file, folder, err := checkShare(ctx, token, password)
	if err != nil {
		return nil, nil, err
	}
	if file != nil {
		log.Infof("share resolve: token %s file %d", token, file.ID)
	} else {
		log.Infof("share resolve: token %s folder %d", token, folder.ID)
	}
	return file, folder, nil
}

// resolveSharedFile 分享通道文件定位(不计数;全量下载 / 元数据定位 / 区间下载三通道共用):
// 分享校验 → 定位文件(文件分享直接用,文件夹分享按 relPath 下钻)→ 祖先链 Isable。
// 错误语义:ErrNotFound / ErrUnauthorized / ErrInvalidInput。
func resolveSharedFile(ctx context.Context, token, password, relPath string) (*model.ShareLink, *model.File, error) {
	// 分享校验(不计数)
	share, file, folder, err := checkShare(ctx, token, password)
	if err != nil {
		return nil, nil, err
	}

	// 定位文件:文件夹分享按 relPath 逐段下钻
	if file == nil {
		file, err = locateSharedFileNew(ctx, folder, relPath) // 文件夹分享:按分享根内相对路径定位
		if err != nil {
			return nil, nil, err
		}
	}

	// 祖先链 Isable(目录已删 → 404)
	if err := checkAncestorsUsable(ctx, file.BucketID, file.FolderID); err != nil {
		return nil, nil, err
	}
	return share, file, nil
}

// DownloadSharedFileArg 分享下载入参。
type DownloadSharedFileArg struct {
	Token    string // 分享短码
	Password string // 提取码明文(无则空串)
	RelPath  string // 文件夹分享:分享根内文件相对路径(空 = ErrInvalidInput)
}

// DownloadSharedFile 分享下载/预览通道(公开访问):校验并自增下载次数后
// 返回 (对象读取流, 文件元数据);调用方负责关闭流。
// 错误语义:分享不存在/过期 → ErrNotFound;提取码错误 → ErrUnauthorized;
// 次数超限 → ErrForbidden;relPath 非法 → ErrInvalidInput。
func DownloadSharedFile(ctx context.Context, arg DownloadSharedFileArg) (io.ReadCloser, *model.File, error) {
	token, password, relPath := arg.Token, arg.Password, arg.RelPath
	// 分享校验 + 定位 + Isable(不计数)
	share, file, err := resolveSharedFile(ctx, token, password, relPath)
	if err != nil {
		return nil, nil, err
	}

	// 下载次数校验与自增(条件更新,防超卖)
	if share.MaxDownloads > 0 {
		res := core.DB.Model(&model.ShareLink{}).
			Where("id = ? AND download_count < max_downloads", share.ID).
			Update("download_count", gorm.Expr("download_count + 1"))
		if res.Error != nil {
			return nil, nil, fmt.Errorf("download shared file: increment count: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return nil, nil, ErrForbidden // 次数已达上限
		}
	}

	// 取桶(桶已删 → 文件不可达)
	bucket, err := GetBucket(ctx, GetBucketArg{ID: file.BucketID})
	if err != nil {
		return nil, nil, err
	}

	// 取对象流(key = 主键 ID)
	rc, err := core.Storage.Get(ctx, utils.BucketEncoder(bucket.ID), objectKeyForFile(file.ID))
	if err != nil {
		if errors.Is(err, core.ErrObjectNotFound) {
			return nil, nil, ErrNotFound // 对象已删
		}
		return nil, nil, err
	}

	log.Infof("share download: token %s file %d (bucket %d) size %d", token, file.ID, file.BucketID, file.FileSize)
	return rc, file, nil
}

// SharedFileMetaArg 分享文件元数据定位入参。
type SharedFileMetaArg struct {
	Token    string // 分享短码
	Password string // 提取码明文(无则空串)
	RelPath  string // 文件夹分享:分享根内文件相对路径
}

// SharedFileMeta 分享通道文件元数据定位(供 api 层解析 Range 前取 FileSize,不计数,
// 不触碰实体对象)。
func SharedFileMeta(ctx context.Context, arg SharedFileMetaArg) (*model.File, error) {
	_, file, err := resolveSharedFile(ctx, arg.Token, arg.Password, arg.RelPath)
	if err != nil {
		return nil, err
	}
	return file, nil
}

// DownloadSharedFileRangeArg 分享区间下载入参。
type DownloadSharedFileRangeArg struct {
	Token    string // 分享短码
	Password string // 提取码明文(无则空串)
	RelPath  string // 文件夹分享:分享根内文件相对路径
	Start    int64  // 区间起点(闭区间,含)
	End      int64  // 区间终点(闭区间,含)
}

// DownloadSharedFileRange 分享通道范围下载(公开访问):与 DownloadSharedFile 同一校验通路,
// 实体读取改走 core.Storage.GetRange 裁剪区间(断点续传/分享视频拖动)。
// start/end 为闭区间两端,由 api 层经 utils.ParseRange 归一,本函数兜底复检:
// 起点越界 → ErrRangeNotSatisfiable(416,不计数),区间非法 → ErrInvalidInput。
// 错误语义:同 DownloadSharedFile,另加 ErrRangeNotSatisfiable。
func DownloadSharedFileRange(ctx context.Context, arg DownloadSharedFileRangeArg) (io.ReadCloser, *model.File, error) {
	token, password, relPath, start, end := arg.Token, arg.Password, arg.RelPath, arg.Start, arg.End
	// 分享校验 + 定位 + Isable(不计数)
	share, file, err := resolveSharedFile(ctx, token, password, relPath)
	if err != nil {
		return nil, nil, err
	}

	// 兜底复检区间(start 越界必须最先判定,否则 416 分支永不触发)
	if start >= file.FileSize {
		return nil, nil, ErrRangeNotSatisfiable // 起点越界 → 416,不消耗次数
	}
	if start < 0 || end < start || end >= file.FileSize {
		return nil, nil, ErrInvalidInput
	}

	// 下载次数校验与自增(条件更新,防超卖)
	if share.MaxDownloads > 0 {
		res := core.DB.Model(&model.ShareLink{}).
			Where("id = ? AND download_count < max_downloads", share.ID).
			Update("download_count", gorm.Expr("download_count + 1"))
		if res.Error != nil {
			return nil, nil, fmt.Errorf("download shared file range: increment count: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return nil, nil, ErrForbidden // 次数已达上限
		}
	}

	// 取桶(桶已删 → 文件不可达)
	bucket, err := GetBucket(ctx, GetBucketArg{ID: file.BucketID})
	if err != nil {
		return nil, nil, err
	}

	// 读对象字节区间
	rc, err := core.Storage.GetRange(ctx, utils.BucketEncoder(bucket.ID), objectKeyForFile(file.ID), start, end)
	if err != nil {
		if errors.Is(err, core.ErrObjectNotFound) {
			return nil, nil, ErrNotFound // 对象已删
		}
		return nil, nil, err
	}

	log.Infof("share download range: token %s file %d (bucket %d) [%d,%d] size %d",
		token, file.ID, file.BucketID, start, end, file.FileSize)
	return rc, file, nil
}

// locateSharedFileNew 文件夹分享下按分享根内相对路径定位文件(仅文件):
// 从分享根逐段下钻(每层等值查),天然防穿越。
func locateSharedFileNew(ctx context.Context, root *model.Folder, relPath string) (*model.File, error) {
	relPath = strings.Trim(strings.TrimSpace(relPath), "/")
	if relPath == "" {
		return nil, ErrInvalidInput // 空路径无法确定下载目标,走 ListSharedDir 浏览
	}
	segs := strings.Split(relPath, "/")
	name := segs[len(segs)-1]
	if err := common.ValidateItemName(name); err != nil {
		return nil, err
	}

	curID := root.ID // 分享根 folder 的 ID
	for i, seg := range segs {
		if i == len(segs)-1 {
			// 最后一段 → 文件
			var f model.File
			err := core.DB.WithContext(ctx).
				Where("bucket_id = ? AND folder_id = ? AND name_lower = ?",
					root.BucketID, curID, strings.ToLower(name)).First(&f).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrNotFound
			}
			if err != nil {
				return nil, fmt.Errorf("locate shared file: %w", err)
			}
			return &f, nil
		}
		// 中间段 → 子文件夹(逐段下钻)
		var sub model.Folder
		err := core.DB.WithContext(ctx).
			Where("bucket_id = ? AND parent_id = ? AND name_lower = ?",
				root.BucketID, curID, strings.ToLower(seg)).First(&sub).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, fmt.Errorf("locate shared file: %w", err)
		}
		curID = sub.ID
	}
	return nil, ErrNotFound // 不可达
}

// ListSharedDirArg 分享目录列表入参。
type ListSharedDirArg struct {
	Token    string // 分享短码
	Password string // 提取码明文(无则空串)
	RelPath  string // 分享根内相对目录路径(空 = 分享根)
	Page     int    // 页码(≥1)
	PageSize int    // 页大小(缺省 50,上限 500)
}

// ListSharedDir 文件夹分享下列出分享根内某目录的条目(公开访问,不计数):
// relPath 空 = 分享根,分页规则与 ListFiles 一致。
// 错误语义:同 ResolveShare;非文件夹分享或越出分享根 → ErrInvalidInput。
func ListSharedDir(ctx context.Context, arg ListSharedDirArg) (files []model.File, folders []model.Folder, total int64, err error) {
	token, password, relPath, page, pageSize := arg.Token, arg.Password, arg.RelPath, arg.Page, arg.PageSize
	_, _, root, err := checkShare(ctx, token, password)
	if err != nil {
		return nil, nil, 0, err
	}
	if root == nil {
		return nil, nil, 0, ErrInvalidInput // 文件分享无目录列表
	}

	// 定位目标目录:沿 relPath 逐段下钻(空 = 分享根)
	targetFolderID := root.ID
	relPath = strings.Trim(strings.TrimSpace(relPath), "/")
	if relPath != "" {
		curID := root.ID
		for _, seg := range strings.Split(relPath, "/") {
			var sub model.Folder
			err := core.DB.WithContext(ctx).
				Where("bucket_id = ? AND parent_id = ? AND name_lower = ?",
					root.BucketID, curID, strings.ToLower(seg)).First(&sub).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil, 0, ErrNotFound // 越出分享根/目录不存在
			}
			if err != nil {
				return nil, nil, 0, fmt.Errorf("list shared dir: locate: %w", err)
			}
			curID = sub.ID
		}
		targetFolderID = curID
	}

	// Isable 校验(目录已删 → 404)
	if err := checkAncestorsUsable(ctx, root.BucketID, targetFolderID); err != nil {
		return nil, nil, 0, err
	}

	// 分页归一 + 双表合并分页(UNION ALL,同 ListFiles;分享通道不设受限过滤)
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 500 {
		pageSize = 500
	}
	unionSQL := `SELECT id, created_at, kind FROM (
		SELECT id, created_at, 'file' AS kind FROM files   WHERE bucket_id = ? AND folder_id = ?
		UNION ALL
		SELECT id, created_at, 'folder' AS kind FROM folders WHERE bucket_id = ? AND parent_id = ?
	) t ORDER BY created_at DESC LIMIT ? OFFSET ?`
	rows := []unionRow{}
	if err := core.DB.WithContext(ctx).Raw(unionSQL,
		root.BucketID, targetFolderID, root.BucketID, targetFolderID, pageSize, (page-1)*pageSize).Scan(&rows).Error; err != nil {
		return nil, nil, 0, fmt.Errorf("list shared dir: union query: %w", err)
	}

	// total = 两表计数之和
	var n1, n2 int64
	if err := core.DB.WithContext(ctx).Model(&model.File{}).
		Where("bucket_id = ? AND folder_id = ?", root.BucketID, targetFolderID).Count(&n1).Error; err != nil {
		return nil, nil, 0, err
	}
	if err := core.DB.WithContext(ctx).Model(&model.Folder{}).
		Where("bucket_id = ? AND parent_id = ?", root.BucketID, targetFolderID).Count(&n2).Error; err != nil {
		return nil, nil, 0, err
	}
	total = n1 + n2

	// 按 kind 批量加载(IN 一次取全,按投影顺序重排;并发删除的行自然跳过)
	files = make([]model.File, 0, len(rows))
	folders = make([]model.Folder, 0, len(rows))
	var fileIDs, folderIDs []uint
	for _, row := range rows {
		if row.Kind == ItemKindFile {
			fileIDs = append(fileIDs, row.ID)
		} else {
			folderIDs = append(folderIDs, row.ID)
		}
	}
	filesByID := map[uint]model.File{}
	if len(fileIDs) > 0 {
		var fs []model.File
		if err := core.DB.WithContext(ctx).Where("id IN ? AND bucket_id = ?", fileIDs, root.BucketID).Find(&fs).Error; err != nil {
			return nil, nil, 0, fmt.Errorf("list shared dir: load files: %w", err)
		}
		for i := range fs {
			filesByID[fs[i].ID] = fs[i]
		}
	}
	foldersByID := map[uint]model.Folder{}
	if len(folderIDs) > 0 {
		var fds []model.Folder
		if err := core.DB.WithContext(ctx).Where("id IN ? AND bucket_id = ?", folderIDs, root.BucketID).Find(&fds).Error; err != nil {
			return nil, nil, 0, fmt.Errorf("list shared dir: load folders: %w", err)
		}
		for i := range fds {
			foldersByID[fds[i].ID] = fds[i]
		}
	}
	for _, row := range rows {
		if row.Kind == ItemKindFile {
			if f, ok := filesByID[row.ID]; ok {
				files = append(files, f)
			}
		} else if f, ok := foldersByID[row.ID]; ok {
			folders = append(folders, f)
		}
	}
	return files, folders, total, nil
}
