// batch.go —— 批量操作:删除 / 复制 / 移动 / 上传。
//
// 参数风格:行式切片 + 批量级默认值继承(条目指针字段 nil = 继承批量级默认值);
// 逐条独立执行,失败项收集进 BatchResultItem.Error,不中断其余;
// 鉴权由 api 层调用前完成,本包不做整体事务。
package server

import (
	"context"
	"io"
	"strings"
)

// 批量操作单次上限(防御;与 CopyTask batch 一致)。
const batchMaxItems = 100

// BatchResultItem 单条目操作结果(成功/失败统一结构)。
type BatchResultItem struct {
	Kind   string // file | folder(源条目类型)
	ID     uint   // 源条目 ID(失败定位用)
	Name   string // 条目名(展示用)
	Data   any    // 成功时的业务对象:复制/上传 → 新 file/folder 记录;删除/下载 → nil
	Error  string // 空 = 成功;非空 = 该条失败原因(HTTP 层逐项提示)
	TaskID uint   // 落后台任务表时返回的任务 ID(api 层经协程池提交执行;0 = 无需提交)
}

// CheckBatchItemsArg 批量入参校验入参。
type CheckBatchItemsArg struct {
	Count int // 条目数
}

// CheckBatchItems 校验批量操作入参:空列表 / 超上限 → ErrInvalidInput(400)。
// 供 api 层批量接口在绑定参数后统一调用。
func CheckBatchItems(ctx context.Context, arg CheckBatchItemsArg) error {
	n := arg.Count
	if n == 0 {
		return ErrInvalidInput
	}
	if n > batchMaxItems {
		return ErrInvalidInput
	}
	return nil
}

// ==================== 1. 批量复制 ====================

// CopyItem 单条复制指令。
type CopyItem struct {
	SrcBucketID uint    `json:"src_bucket_id"` // 源桶 ID(0 视为非法)
	Kind        string  `json:"kind"`          // ItemKindFile(同步复制)| ItemKindFolder(落 CopyTask 后台)
	SrcID       uint    `json:"id"`            // 源条目 ID(files.id / folders.id)
	DstBucketID *uint   `json:"dst_bucket_id"` // 目标桶;nil = 继承批量级默认
	DstDir      *string `json:"dst_dir"`       // 目标目录;nil = 继承批量级默认
	DstName     *string `json:"dst_name"`      // 目标名;nil = 沿用源名
}

// CopyItemsArg 批量复制参数。
type CopyItemsArg struct {
	UserID      uint       // 操作者(api 层注入)
	SrcBucketID uint       // 批量级默认源桶(条目未显式指定 src_bucket_id 时继承)
	DstBucketID uint       // 批量级默认目标桶
	DstDir      string     // 批量级默认目标目录
	Items       []CopyItem // 复制指令列表(≥1 且 ≤ 批量上限)
}

// CopyItems 批量复制:文件同步复制(新记录+新对象),文件夹落 CopyTask 后台深度优先。
// 逐条独立执行,失败只进对应项;复制结果继承源可见组;目标名冲突自动重命名。
func CopyItems(ctx context.Context, arg CopyItemsArg) []BatchResultItem {
	results := make([]BatchResultItem, 0, len(arg.Items))
	for _, it := range arg.Items {
		// 缺省值解析:源桶/目标桶/目录 = 批量级默认(条目显式指定优先)
		srcBucket := arg.SrcBucketID
		if it.SrcBucketID != 0 {
			srcBucket = it.SrcBucketID
		}
		dstBucket := arg.DstBucketID
		if it.DstBucketID != nil {
			dstBucket = *it.DstBucketID
		}
		dstDir := arg.DstDir
		if it.DstDir != nil {
			dstDir = *it.DstDir
		}
		dstName := ""
		if it.DstName != nil {
			dstName = *it.DstName
		}

		switch it.Kind {
		case ItemKindFile:
			f, err := CopyFile(ctx, CopyFileArg{UserID: arg.UserID, SrcBucketID: srcBucket, SrcFileID: it.SrcID, DstBucketID: dstBucket, DstDirPath: dstDir, DstFilename: dstName})
			if err != nil {
				results = append(results, BatchResultItem{Kind: ItemKindFile, ID: it.SrcID, Error: err.Error()})
				continue
			}
			results = append(results, BatchResultItem{Kind: ItemKindFile, ID: it.SrcID, Name: f.Name, Data: f})
		case ItemKindFolder:
			fd, err := CopyFolder(ctx, CopyFolderArg{UserID: arg.UserID, SrcBucketID: srcBucket, SrcFolderID: it.SrcID, DstBucketID: dstBucket, DstDirPath: dstDir, DstName: dstName})
			if err != nil {
				results = append(results, BatchResultItem{Kind: ItemKindFolder, ID: it.SrcID, Error: err.Error()})
				continue
			}
			results = append(results, BatchResultItem{Kind: ItemKindFolder, ID: it.SrcID, Name: fd.Name, Data: fd})
		default:
			results = append(results, BatchResultItem{Kind: it.Kind, ID: it.SrcID, Error: "invalid kind: " + it.Kind})
		}
	}
	return results
}

// ==================== 2. 批量移动/剪切 ====================

// MoveItem 单条移动指令。
type MoveItem struct {
	SrcBucketID uint    `json:"src_bucket_id"` // 源桶 ID(0 视为非法)
	Kind        string  `json:"kind"`          // ItemKindFile | ItemKindFolder
	SrcID       uint    `json:"id"`            // 源条目 ID
	DstBucketID *uint   `json:"dst_bucket_id"` // 目标桶;nil = 继承批量级默认(缺省同桶)
	DstDir      *string `json:"dst_dir"`       // 目标目录;nil = 继承批量级默认
	DstName     *string `json:"dst_name"`      // 新名;nil = 沿用源名
}

// MoveItemsArg 批量移动/剪切参数。
type MoveItemsArg struct {
	UserID      uint
	SrcBucketID uint       // 批量级默认源桶(条目未显式指定 src_bucket_id 时继承)
	DstBucketID uint       // 批量级默认目标桶(0 = 同桶)
	DstDir      string     // 批量级默认目标目录
	Items       []MoveItem // 移动指令列表
}

// MoveItems 批量移动/剪切:文件同桶 O(1) 更新元数据,跨桶复制+删源;
// 文件夹仅支持同桶;移动到自身子树或目标名冲突的项进 failed,不中断其余。
func MoveItems(ctx context.Context, arg MoveItemsArg) []BatchResultItem {
	results := make([]BatchResultItem, 0, len(arg.Items))
	for _, it := range arg.Items {
		srcBucket := arg.SrcBucketID
		if it.SrcBucketID != 0 {
			srcBucket = it.SrcBucketID
		}
		dstBucket := arg.DstBucketID
		if it.DstBucketID != nil {
			dstBucket = *it.DstBucketID
		}
		dstDir := arg.DstDir
		if it.DstDir != nil {
			dstDir = *it.DstDir
		}
		dstName := ""
		if it.DstName != nil {
			dstName = *it.DstName
		}

		switch it.Kind {
		case ItemKindFile:
			f, err := MoveFile(ctx, MoveFileArg{UserID: arg.UserID, SrcBucketID: srcBucket, SrcFileID: it.SrcID, DstBucketID: dstBucket, DstDirPath: dstDir, DstName: dstName})
			if err != nil {
				results = append(results, BatchResultItem{Kind: ItemKindFile, ID: it.SrcID, Error: err.Error()})
				continue
			}
			results = append(results, BatchResultItem{Kind: ItemKindFile, ID: it.SrcID, Name: f.Name, Data: f})
		case ItemKindFolder:
			fd, err := MoveFolder(ctx, MoveFolderArg{UserID: arg.UserID, SrcBucketID: srcBucket, SrcFolderID: it.SrcID, DstBucketID: dstBucket, DstDirPath: dstDir, DstName: dstName})
			if err != nil {
				results = append(results, BatchResultItem{Kind: ItemKindFolder, ID: it.SrcID, Error: err.Error()})
				continue
			}
			results = append(results, BatchResultItem{Kind: ItemKindFolder, ID: it.SrcID, Name: fd.Name, Data: fd})
		default:
			results = append(results, BatchResultItem{Kind: it.Kind, ID: it.SrcID, Error: "invalid kind: " + it.Kind})
		}
	}
	return results
}

// ==================== 3. 批量删除 ====================

// DeleteItem 单条删除指令。
type DeleteItem struct {
	Kind string `json:"kind"` // ItemKindFile | ItemKindFolder
	ID   uint   `json:"id"`   // files.id / folders.id
}

// DeleteItemsArg 批量删除参数。
type DeleteItemsArg struct {
	UserID   uint         // 操作者
	BucketID uint         // 所属桶(删除限单桶)
	Items    []DeleteItem // 删除指令列表
}

// DeleteItems 批量删除(同桶):文件/文件夹删除进回收站(软删置 deleted_at,
// 保留记录/对象/用量;文件夹另置 Isable=false),逐条独立,失败不中断其余。
func DeleteItems(ctx context.Context, arg DeleteItemsArg) []BatchResultItem {
	results := make([]BatchResultItem, 0, len(arg.Items))
	for _, it := range arg.Items {
		switch it.Kind {
		case ItemKindFile:
			if err := DeleteFile(ctx, DeleteFileArg{UserID: arg.UserID, BucketID: arg.BucketID, FileID: it.ID}); err != nil {
				results = append(results, BatchResultItem{Kind: ItemKindFile, ID: it.ID, Error: err.Error()})
				continue
			}
			results = append(results, BatchResultItem{Kind: ItemKindFile, ID: it.ID})
		case ItemKindFolder:
			taskID, err := DeleteDir(ctx, DeleteDirArg{UserID: arg.UserID, BucketID: arg.BucketID, DirID: it.ID})
			if err != nil {
				results = append(results, BatchResultItem{Kind: ItemKindFolder, ID: it.ID, Error: err.Error()})
				continue
			}
			results = append(results, BatchResultItem{Kind: ItemKindFolder, ID: it.ID, TaskID: taskID})
		default:
			results = append(results, BatchResultItem{Kind: it.Kind, ID: it.ID, Error: "invalid kind: " + it.Kind})
		}
	}
	return results
}

// ==================== 4. 批量上传 ====================

// UploadItem 单文件上传体(api 层从 multipart 逐个传入,流式不整读内存)。
type UploadItem struct {
	Name   string    // 文件名(multipart 原名)
	Reader io.Reader // 文件流
}

// UploadFilesArg 批量上传参数。
type UploadFilesArg struct {
	UserID   uint         // 操作者
	BucketID uint         // 目标桶
	DirPath  string       // 目标目录路径(mkdir -p;与 FolderID 二选一)
	FolderID uint         // 目标目录 ID 直传,跳过路径解析(O(1);0 = 使用 DirPath)
	Items    []UploadItem // 待上传文件列表
}

// UploadFiles 批量上传到统一目录:逐文件独立落库并写对象,桶 used_space 原子自增;
// 单文件失败进 failed,不中断其余。
func UploadFiles(ctx context.Context, arg UploadFilesArg) []BatchResultItem {
	results := make([]BatchResultItem, 0, len(arg.Items))
	for _, it := range arg.Items {
		dirPath := arg.DirPath
		// FolderID 直传:按 ID 定位目录;失败不得静默回退桶根(否则绕过目录 ACL),该项进 failed。
		if arg.FolderID != 0 {
			var err error
			dirPath, err = folderIDToPath(ctx, arg.UserID, arg.BucketID, arg.FolderID)
			if err != nil {
				results = append(results, BatchResultItem{Kind: ItemKindFile, ID: 0, Name: it.Name, Error: err.Error()})
				continue
			}
		}
		f, err := UploadFile(ctx, UploadFileArg{UserID: arg.UserID, BucketID: arg.BucketID, DirPath: dirPath, Filename: it.Name, Reader: it.Reader})
		if err != nil {
			results = append(results, BatchResultItem{Kind: ItemKindFile, ID: 0, Name: it.Name, Error: err.Error()})
			continue
		}
		results = append(results, BatchResultItem{Kind: ItemKindFile, ID: f.ID, Name: f.Name, Data: f})
	}
	return results
}

// folderIDToPath 按目录 ID 反推路径(仅供 FolderID 直传时构造上传目标)。
func folderIDToPath(ctx context.Context, userID, bucketID, folderID uint) (string, error) {
	// 校验目标目录存在/可用/可见
	if folderID != 0 {
		if err := checkAncestorsAccessTree(ctx, userID, bucketID, folderID); err != nil {
			return "", err
		}
	}
	// 沿 parent 链上溯拼接路径(桶根 folderID=0 → "/")
	segs := []string{}
	cur := folderID
	for cur != 0 {
		f, err := loadFolder(ctx, bucketID, cur)
		if err != nil {
			return "", err
		}
		segs = append([]string{f.Name}, segs...)
		cur = f.ParentID
	}
	if len(segs) == 0 {
		return "/", nil
	}
	return "/" + strings.Join(segs, "/"), nil
}
