// file_delete.go —— 桶内条目删除:文件删除进回收站(软删);文件夹删除落删除任务。
//
// 回收站语义:
//   - 文件删除 = 置 gorm.Model.DeletedAt(deleted_at 列)= now,保留记录/对象/用量,
//     不归还空间(软删不归还用量,与 cron 对账口径一致);回收站清除(后续功能)
//     才物理删除并归还;
//   - 读取/列表/搜索路径全部经 GORM 软删自动过滤(deleted_at IS NULL),
//     回收站内条目不可见不可达;
//   - 文件夹删除:落 DeleteTask(带操作者/权限快照)后后台处理——文件夹自身
//     保持可见,深度优先先删内部(逐条软删进回收站),内部清空后才置 Isable=false
//     + 软删删除文件夹自身(见 bucket_task.go processDeleteDirTask);
//   - 级联:删除指向被删条目的分享链接(删除即不可再访问)。
//
// 条目权限由 api 层预检,本文件只做可行性(桶状态、条目归属、Isable 链);
// userID 用于操作日志 + 任务操作者快照。
package server

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"orbitcloud/core"
	"orbitcloud/log"
	"orbitcloud/model"
)

// DeleteFileArg 文件删除入参。
type DeleteFileArg struct {
	UserID   uint // 操作者 users.id(操作日志)
	BucketID uint // 所属桶
	FileID   uint // 文件 files.id
}

// DeleteFile 删除文件条目 → 进回收站:校验桶与条目 → 事务内软删记录
// (置 deleted_at=now,保留对象/用量)+ 级联清理指向该文件的分享。
// 错误语义:文件不存在/已进回收站/祖先目录已删 → ErrNotFound;桶禁用 → ErrForbidden。
func DeleteFile(ctx context.Context, arg DeleteFileArg) error {
	userID, bucketID, fileID := arg.UserID, arg.BucketID, arg.FileID
	// 桶对象状态(存在 + Status==1)
	if _, err := CheckBucketUsable(ctx, CheckBucketUsableArg{BucketID: bucketID}); err != nil {
		return err
	}

	// 查文件(软删过滤:回收站内再删 → ErrNotFound)
	file, err := loadFile(ctx, bucketID, fileID)
	if err != nil {
		return err
	}

	// 祖先链 Isable(目录已删 → 404)
	if err := checkAncestorsUsable(ctx, bucketID, file.FolderID); err != nil {
		return err
	}

	// 回收站:软删(置 deleted_at=now)+ 级联清理分享
	if err := core.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&model.File{}, fileID).Error; err != nil {
			return err
		}
		// 级联:删除指向该文件的分享
		if err := tx.Unscoped().
			Where("bucket_item_id = ? AND item_type = 'file'", fileID).
			Delete(&model.ShareLink{}).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}

	log.Infof("delete file: user %d bucket %d file %d (%q) moved to recycle bin", userID, bucketID, fileID, file.Name)
	return nil
}

// DeleteDirArg 文件夹删除入参。
type DeleteDirArg struct {
	UserID   uint // 操作者 users.id(操作日志 + 任务操作者快照)
	BucketID uint // 所属桶
	DirID    uint // 文件夹 folders.id
}

// DeleteDir 删除文件夹:落 DeleteTask(带操作者/权限快照)后触发后台任务。
// 关键语义:落表后文件夹置 FolderDeleting(正在删除)——文件夹**保持可见可读**,
// 但拒绝一切新写入(上传/建目录/移动/复制进来,见 CheckAncestorsWritable);
// 后台任务深度优先先删内部(文件/子文件夹逐条软删进回收站),内部全部清空后
// 才删除文件夹自身(置 FolderUnusable + 软删)。中断时子树结构完整,可续跑/恢复。
// 错误语义:目录不存在/已进回收站 → ErrNotFound;桶禁用 → ErrForbidden。
func DeleteDir(ctx context.Context, arg DeleteDirArg) (uint, error) {
	userID, bucketID, dirID := arg.UserID, arg.BucketID, arg.DirID
	// 桶对象状态(存在 + Status==1)
	if _, err := CheckBucketUsable(ctx, CheckBucketUsableArg{BucketID: bucketID}); err != nil {
		return 0, err
	}

	// 查目录(软删过滤:回收站内再删 → ErrNotFound)
	folder, err := loadFolder(ctx, bucketID, dirID)
	if err != nil {
		return 0, err
	}
	// 已在删除中(任务未完成)→ 拒绝重复删除
	if folder.Isable == model.FolderDeleting {
		return 0, ErrForbidden
	}

	// 权限快照(处理时动态重查,此处仅记录发起时等级用于审计/语义判断)
	operatorLevel := operatorLevelOf(ctx, userID)

	// 落删除任务(文件夹保持可见,后台先删内部再删自身)
	task := &model.DeleteTask{
		BucketID:      bucketID,
		DirID:         dirID,
		OperatorID:    userID,
		OperatorLevel: operatorLevel,
		Status:        0,
	}
	if err := core.DB.WithContext(ctx).Create(task).Error; err != nil {
		return 0, fmt.Errorf("delete dir %d: create delete task: %w", dirID, err)
	}

	// 置 FolderDeleting:正在删除(可见可读,拒写)——拒绝删除期间新写入的竞态
	if err := core.DB.WithContext(ctx).Model(&model.Folder{}).
		Where("id = ?", dirID).Update("isable", model.FolderDeleting).Error; err != nil {
		return 0, fmt.Errorf("delete dir %d: mark deleting: %w", dirID, err)
	}

	// 触发处理(尽力而为,失败不阻断返回——残留任务由启动/cron 续跑)
	if err := ProcessDeleteTask(ctx, ProcessDeleteTaskArg{TaskID: task.ID}); err != nil {
		log.Errorf("delete dir %d: process task %d: %v", dirID, task.ID, err)
	}

	log.Infof("delete dir: user %d bucket %d dir %d delete task %d queued", userID, bucketID, dirID, task.ID)
	return task.ID, nil
}
