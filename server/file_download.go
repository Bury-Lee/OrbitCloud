// file_download.go —— 桶内条目读操作:文件下载 / 文件元数据 / 文件夹元数据。
//
// 本文件只做对象维度可行性:桶状态、条目归属、祖先链 Isable、Range 区间合法;
// 用户维度权限(桶等级 / 条目 ACL / 祖先链 ACL)由 api 层预检。
package server

import (
	"context"
	"errors"
	"io"

	"orbitcloud/core"
	"orbitcloud/log"
	"orbitcloud/model"
	"orbitcloud/utils"
)

// CheckFileDownloadArg 文件下载可行性校验入参。
type CheckFileDownloadArg struct {
	BucketID uint // 所属桶
	FileID   uint // 文件 files.id
}

// CheckFileDownload 文件下载可行性校验(DownloadFile / DownloadFileRange / api 层预检共用):
// 桶可用 → 文件存在且属于该桶 → 祖先链 Isable,返回 (桶, 文件) 供调用方复用。
// 错误语义:ErrNotFound / ErrForbidden。
func CheckFileDownload(ctx context.Context, arg CheckFileDownloadArg) (*model.Bucket, *model.File, error) {
	bucketID, fileID := arg.BucketID, arg.FileID
	// 桶可用性(存在 + 状态正常)
	bucket, err := CheckBucketUsable(ctx, CheckBucketUsableArg{BucketID: bucketID})
	if err != nil {
		return nil, nil, err
	}

	// 查文件
	file, err := loadFile(ctx, bucketID, fileID)
	if err != nil {
		return nil, nil, err
	}

	// 祖先链 Isable(目录已删 → 404)
	if err := checkAncestorsUsable(ctx, bucketID, file.FolderID); err != nil {
		return nil, nil, err
	}
	return bucket, file, nil
}

// DownloadFileArg 文件下载入参。
type DownloadFileArg struct {
	BucketID uint // 所属桶
	FileID   uint // 文件 files.id
}

// DownloadFile 下载文件:可行性校验后经 core.Storage.Get 读对象流,
// 返回 (对象读取流, 文件元数据);调用方负责关闭流。
// 错误语义:文件不存在/祖先目录已删 → ErrNotFound;桶禁用 → ErrForbidden。
func DownloadFile(ctx context.Context, arg DownloadFileArg) (io.ReadCloser, *model.File, error) {
	bucketID, fileID := arg.BucketID, arg.FileID
	// 可行性校验
	_, file, err := CheckFileDownload(ctx, CheckFileDownloadArg{BucketID: bucketID, FileID: fileID})
	if err != nil {
		return nil, nil, err
	}

	// 读对象流(悬空记录对象缺失 → ErrObjectNotFound → ErrNotFound)
	rc, err := core.Storage.Get(ctx, utils.BucketEncoder(file.BucketID), objectKeyForFile(file.ID))
	if err != nil {
		if errors.Is(err, core.ErrObjectNotFound) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}

	log.Infof("download: bucket %d file %d (%q) size %d", bucketID, fileID, file.Name, file.FileSize)
	return rc, file, nil
}

// DownloadFileRangeArg 文件区间下载入参。
type DownloadFileRangeArg struct {
	BucketID uint  // 所属桶
	FileID   uint  // 文件 files.id
	Start    int64 // 区间起点(闭区间,含)
	End      int64 // 区间终点(闭区间,含)
}

// DownloadFileRange 下载文件指定字节区间(断点续传/音视频流式播放)。
// start/end 为闭区间两端,由 api 层经 utils.ParseRange 归一(0 ≤ start ≤ end < FileSize),
// 本函数仅兜底复检:起点越界 → ErrRangeNotSatisfiable(416),区间非法 → ErrInvalidInput;
// 区间读取走 core.Storage.GetRange。错误语义:ErrNotFound / ErrForbidden / 416 / 400。
func DownloadFileRange(ctx context.Context, arg DownloadFileRangeArg) (io.ReadCloser, *model.File, error) {
	bucketID, fileID, start, end := arg.BucketID, arg.FileID, arg.Start, arg.End
	// 可行性校验
	_, file, err := CheckFileDownload(ctx, CheckFileDownloadArg{BucketID: bucketID, FileID: fileID})
	if err != nil {
		return nil, nil, err
	}

	// 兜底复检区间(start >= size 判定须在 end >= size 之前,否则 416 分支永不触发)
	if start >= file.FileSize {
		return nil, nil, ErrRangeNotSatisfiable // 起点越界 → 416
	}
	if start < 0 || end < start || end >= file.FileSize {
		return nil, nil, ErrInvalidInput
	}

	// 读对象字节区间
	rc, err := core.Storage.GetRange(ctx, utils.BucketEncoder(file.BucketID), objectKeyForFile(file.ID), start, end)
	if err != nil {
		if errors.Is(err, core.ErrObjectNotFound) {
			return nil, nil, ErrNotFound // 悬空记录兜底
		}
		return nil, nil, err
	}

	log.Infof("download range: bucket %d file %d (%q) [%d,%d] size %d",
		bucketID, fileID, file.Name, start, end, file.FileSize)
	return rc, file, nil
}

// GetFolderMetaArg 文件夹元数据查询入参。
type GetFolderMetaArg struct {
	BucketID uint // 所属桶
	FolderID uint // 文件夹 folders.id
}

// GetFolderMeta 查询文件夹元数据(仅 folders 表,文件走 GetFileMeta):
// 桶可用 → 文件夹存在且属于该桶 → 自身与祖先链 Isable。
// 错误语义:ErrNotFound / ErrForbidden。
func GetFolderMeta(ctx context.Context, arg GetFolderMetaArg) (*model.Folder, error) {
	bucketID, folderID := arg.BucketID, arg.FolderID
	// 桶可用性
	if _, err := CheckBucketUsable(ctx, CheckBucketUsableArg{BucketID: bucketID}); err != nil {
		return nil, err
	}

	// 查文件夹
	folder, err := loadFolder(ctx, bucketID, folderID)
	if err != nil {
		return nil, err
	}

	// 自身与祖先链 Isable(已删 → 404)
	if err := checkAncestorsUsable(ctx, bucketID, folder.ID); err != nil {
		return nil, err
	}
	return folder, nil
}
