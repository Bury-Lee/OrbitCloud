// download_task.go —— 下载任务业务层:文件夹下载任务的登记 / 查询 / 删除。
//
// 任务仅记录下载起点(FolderID + 路径快照),下载执行由前端主导;
// 中断后前端凭任务 id 恢复,完成后 DELETE 清理;任务归属由 api 层校验,
// 本文件只做可行性判断(任务/文件夹存在)。
package server

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"orbitcloud/core"
	"orbitcloud/log"
	"orbitcloud/model"
)

// CreateDownloadTaskArg 下载任务登记入参。
type CreateDownloadTaskArg struct {
	UserID   uint // 任务归属者 users.id(api 层归属校验依据)
	BucketID uint // 下载起点所在桶
	FolderID uint // 下载起点文件夹 folders.id
}

// CreateDownloadTask 登记文件夹下载任务:校验文件夹可用(桶/Isable 链)后
// 落库 DownloadTask{UserID, BucketID, FolderID, FilePath 快照},返回含主键记录。
// 任务超期由 cron 周期清理(cron.CleanExpiredDownloadTasks),客户端凭任务 id 恢复。
// 错误语义:ErrNotFound / ErrForbidden(桶禁用)。
func CreateDownloadTask(ctx context.Context, arg CreateDownloadTaskArg) (*model.DownloadTask, error) {
	userID, bucketID, folderID := arg.UserID, arg.BucketID, arg.FolderID
	// 可行性校验(桶/文件夹/Isable)
	if _, err := GetFolderMeta(ctx, GetFolderMetaArg{BucketID: bucketID, FolderID: folderID}); err != nil {
		return nil, err
	}

	// 派生路径快照(仅供展示,可能过期)
	path, err := folderIDToPath(ctx, userID, bucketID, folderID)
	if err != nil {
		return nil, err
	}

	task := model.DownloadTask{
		UserID:   userID,
		BucketID: bucketID,
		FolderID: folderID,
		FilePath: path,
	}
	if err := core.DB.WithContext(ctx).Create(&task).Error; err != nil {
		return nil, fmt.Errorf("create download task: %w", err)
	}
	log.Infof("create download task: task=%d user=%d bucket=%d folder=%d path=%q",
		task.ID, userID, bucketID, folderID, path)

	return &task, nil
}

// DeleteDownloadTaskArg 下载任务删除入参。
type DeleteDownloadTaskArg struct {
	TaskID uint // 下载任务 download_tasks.id
}

// DeleteDownloadTask 删除下载任务(完成/取消清理,硬删不留历史;归属校验在 api 层)。
// 错误语义:任务不存在 → ErrNotFound。
func DeleteDownloadTask(ctx context.Context, arg DeleteDownloadTaskArg) error {
	taskID := arg.TaskID
	// 查任务(不存在 → ErrNotFound)
	if _, err := LoadDownloadTask(ctx, LoadDownloadTaskArg{TaskID: taskID}); err != nil {
		return err
	}
	if err := core.DB.WithContext(ctx).Unscoped().Delete(&model.DownloadTask{}, taskID).Error; err != nil {
		return fmt.Errorf("delete download task: %w", err)
	}
	log.Infof("delete download task: task %d deleted", taskID)
	return nil
}

// LoadDownloadTaskArg 下载任务加载入参。
type LoadDownloadTaskArg struct {
	TaskID uint // 下载任务 download_tasks.id
}

// LoadDownloadTask 按主键加载下载任务(可行性:存在;供 api 层归属校验共用)。
// 错误语义:不存在 → ErrNotFound。
func LoadDownloadTask(ctx context.Context, arg LoadDownloadTaskArg) (*model.DownloadTask, error) {
	taskID := arg.TaskID
	var task model.DownloadTask
	err := core.DB.WithContext(ctx).First(&task, taskID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load download task %d: %w", taskID, err)
	}
	return &task, nil
}
