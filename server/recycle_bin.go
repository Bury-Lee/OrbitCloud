// recycle_bin.go —— 回收站过期条目物理清除(cron 周期调用)。
//
// 软删(进回收站)的条目保留记录/对象/用量;超过配置保留期后由本文件
// 物理清除:删对象 + 硬删记录 + 归还桶/用户用量 + 级联删分享。
// 目录按整棵子树清除(子孙条目未逐条标记,不可达但仍占用量)。
// 与 DeleteTask 物理清理共用 objectKeyForFile / returnUserUsedSpace 等工具,
// 本路径无任务锁(单进程 cron 调用,幂等可重入)。
package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"orbitcloud/core"
	"orbitcloud/log"
	"orbitcloud/model"
	"orbitcloud/utils"
)

// recyclePurgeBatch 单批扫描上限(防一次查询过载;循环直到清完)。
const recyclePurgeBatch = 100

// GetRecycledFileArg 回收站文件预检入参。
type GetRecycledFileArg struct {
	FileID uint // 回收站文件 files.id
}

// GetRecycledFile 按 ID 加载回收站文件(软删行,Unscoped 越过 GORM 软删过滤)。
// 供 api 层预检:存在性 + 读取 UploadedBy 做归属校验。
// 错误语义:文件不存在或不在回收站 → ErrNotFound。
func GetRecycledFile(ctx context.Context, arg GetRecycledFileArg) (*model.File, error) {
	var f model.File
	if err := core.DB.WithContext(ctx).Unscoped().
		Where("id = ? AND deleted_at IS NOT NULL", arg.FileID).First(&f).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get recycled file %d: %w", arg.FileID, err)
	}
	return &f, nil
}

// DeleteRecycledFileArg 回收站文件永久删除入参。
type DeleteRecycledFileArg struct {
	OperatorID uint // 操作者 users.id(操作日志)
	FileID     uint // 回收站文件 files.id
}

// DeleteRecycledFile 永久删除回收站文件(提前清除,不等保留期):
// 删对象 + 硬删记录 + 归还桶/用户用量 + 级联删分享(复用 purgeRecycledFile)。
// 权限校验(归属者或管理员)由 api 层预检,本函数只做可行性:文件须在回收站(软删行)。
// 错误语义:文件不存在或不在回收站 → ErrNotFound。
func DeleteRecycledFile(ctx context.Context, arg DeleteRecycledFileArg) error {
	// 可行性:软删行 = 回收站条目
	var f model.File
	if err := core.DB.WithContext(ctx).Unscoped().
		Where("id = ? AND deleted_at IS NOT NULL", arg.FileID).First(&f).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("delete recycled file %d: query: %w", arg.FileID, err)
	}

	if err := purgeRecycledFile(ctx, &f); err != nil {
		return err
	}
	log.Infof("delete recycled file: operator %d file %d purged (recycle bin early clear)", arg.OperatorID, arg.FileID)
	return nil
}

// CleanExpiredRecycleBinArg 回收站过期清理入参。
type CleanExpiredRecycleBinArg struct {
	Cutoff time.Time // 清理 deleted_at < cutoff 的回收站条目
}

// CleanExpiredRecycleBin 物理清除超过保留期的回收站条目。
// 先清过期文件(逐条),再清过期目录(整棵子树深度优先);分批 keyset 游标累计
// (id 升序,每批只扫游标之后增量,避免从头全表扫描)。返回清除的文件/文件夹行数;
// 单条失败即中止(已清部分生效,下次续跑)。
func CleanExpiredRecycleBin(ctx context.Context, arg CleanExpiredRecycleBinArg) (files, folders int64, err error) {
	cutoff := arg.Cutoff

	// 1. 过期文件(软删行,deleted_at < cutoff):分批逐条物理清除
	var lastFileID uint
	for {
		var rows []model.File
		q := core.DB.WithContext(ctx).Unscoped().Model(&model.File{}).
			Where("deleted_at IS NOT NULL AND deleted_at < ?", cutoff)
		if lastFileID > 0 {
			q = q.Where("id > ?", lastFileID)
		}
		if err := q.Order("id ASC").Limit(recyclePurgeBatch).Find(&rows).Error; err != nil {
			return files, folders, fmt.Errorf("clean recycle bin: query files: %w", err)
		}
		if len(rows) == 0 {
			break
		}
		for i := range rows {
			if err := purgeRecycledFile(ctx, &rows[i]); err != nil {
				return files, folders, err
			}
			files++
		}
		lastFileID = rows[len(rows)-1].ID
		if len(rows) < recyclePurgeBatch {
			break // 不足一批 = 已清完
		}
	}

	// 2. 过期目录(软删行):整棵子树物理清除(含未逐条标记的子孙)
	var lastFolderID uint
	for {
		var rows []model.Folder
		q := core.DB.WithContext(ctx).Unscoped().Model(&model.Folder{}).
			Where("deleted_at IS NOT NULL AND deleted_at < ?", cutoff)
		if lastFolderID > 0 {
			q = q.Where("id > ?", lastFolderID)
		}
		if err := q.Order("id ASC").Limit(recyclePurgeBatch).Find(&rows).Error; err != nil {
			return files, folders, fmt.Errorf("clean recycle bin: query folders: %w", err)
		}
		if len(rows) == 0 {
			break
		}
		for i := range rows {
			if err := purgeRecycledFolderSubtree(ctx, rows[i].BucketID, rows[i].ID); err != nil {
				return files, folders, err
			}
			folders++
		}
		lastFolderID = rows[len(rows)-1].ID
		if len(rows) < recyclePurgeBatch {
			break
		}
	}

	log.Infof("clean recycle bin: purged %d files, %d folders (cutoff %s)", files, folders, cutoff)
	return files, folders, nil
}

// purgeRecycledFile 物理删除单个回收站文件:删对象(幂等)→ 事务硬删记录、
// 归还桶/用户用量、级联删分享。
func purgeRecycledFile(ctx context.Context, file *model.File) error {
	// 删对象(幂等;其他错误记日志继续,避免"删不掉"死锁——与历史 DeleteFile 语义一致)
	if err := core.Storage.Delete(ctx, utils.BucketEncoder(file.BucketID), objectKeyForFile(file.ID)); err != nil && !errors.Is(err, core.ErrObjectNotFound) {
		log.Errorf("purge recycled file %d: delete object: %v", file.ID, err)
	}

	if err := core.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Delete(&model.File{}, file.ID).Error; err != nil {
			return err
		}
		// 级联:删除指向该文件的分享
		if err := tx.Unscoped().
			Where("bucket_item_id = ? AND item_type = 'file'", file.ID).
			Delete(&model.ShareLink{}).Error; err != nil {
			return err
		}
		// 归还桶用量(钳制不为负;桶可能已被整桶删除,不存在则跳过)
		var b model.Bucket
		if err := tx.First(&b, file.BucketID).Error; err == nil {
			used := b.UsedSpace - file.FileSize
			if used < 0 {
				used = 0
			}
			if err := tx.Model(&model.Bucket{}).Where("id = ?", file.BucketID).Update("used_space", used).Error; err != nil {
				return err
			}
		}
		// 归还用户用量(按文件归属者 UploadedBy)
		return returnUserUsedSpace(tx, file.UploadedBy, file.FileSize)
	}); err != nil {
		return fmt.Errorf("purge recycled file %d: %w", file.ID, err)
	}
	return nil
}

// purgeRecycledFolderSubtree 物理删除回收站目录的整棵子树:
// 深度优先逐文件夹删其下文件(对象+记录)再删自身,子孙条目一律清除
// (含未逐条标记的、以及目录删除前已单独软删的文件行)。
func purgeRecycledFolderSubtree(ctx context.Context, bucketID, folderID uint) error {
	stack := []uint{folderID}
	for len(stack) > 0 {
		fid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		// 子文件夹压栈(先压后弹 = 深度优先)
		children, err := nextSubfolders(ctx, bucketID, fid)
		if err != nil {
			return err
		}
		for i := range children {
			stack = append(stack, children[i].ID)
		}

		// 先删对象,全部成功才硬删记录(Unscoped 含本目录下已单独软删的文件行)
		files := []model.File{}
		if err := core.DB.WithContext(ctx).Unscoped().
			Where("bucket_id = ? AND folder_id = ?", bucketID, fid).Find(&files).Error; err != nil {
			return fmt.Errorf("purge recycled folder %d: query files: %w", folderID, err)
		}
		var folderSize int64               // 本文件夹文件总大小
		byUser := map[uint]int64{}         // 用户用量归还汇总(按 UploadedBy 分组)
		fileIDs := make([]uint, 0, len(files))
		for i := range files {
			if err := core.Storage.Delete(ctx, utils.BucketEncoder(bucketID), objectKeyForFile(files[i].ID)); err != nil && !errors.Is(err, core.ErrObjectNotFound) {
				log.Errorf("purge recycled folder %d: delete object %s: %v", folderID, objectKeyForFile(files[i].ID), err)
			}
			fileIDs = append(fileIDs, files[i].ID)
			folderSize += files[i].FileSize
			byUser[files[i].UploadedBy] += files[i].FileSize
		}

		// 事务:硬删文件记录 + 级联删分享 + 硬删文件夹自身 + 归还桶/用户用量
		if err := core.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if len(fileIDs) > 0 {
				if err := tx.Unscoped().Where("id IN ?", fileIDs).Delete(&model.File{}).Error; err != nil {
					return err
				}
				// 级联:删除指向这些文件的分享
				if err := tx.Unscoped().
					Where("bucket_item_id IN ? AND item_type = 'file'", fileIDs).
					Delete(&model.ShareLink{}).Error; err != nil {
					return err
				}
			}
			if err := tx.Unscoped().Delete(&model.Folder{}, fid).Error; err != nil {
				return err
			}
			// 级联:删除指向该目录的分享
			if err := tx.Unscoped().
				Where("bucket_item_id = ? AND item_type = 'folder'", fid).
				Delete(&model.ShareLink{}).Error; err != nil {
				return err
			}
			// 归还桶用量(钳制不为负;桶不存在则跳过)
			var b model.Bucket
			if err := tx.First(&b, bucketID).Error; err == nil {
				used := b.UsedSpace - folderSize
				if used < 0 {
					used = 0
				}
				if err := tx.Model(&model.Bucket{}).Where("id = ?", bucketID).Update("used_space", used).Error; err != nil {
					return err
				}
			}
			// 归还用户用量(按文件归属者分组)
			for uid, sz := range byUser {
				if err := returnUserUsedSpace(tx, uid, sz); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return fmt.Errorf("purge recycled folder %d: delete folder %d: %w", folderID, fid, err)
		}
	}
	return nil
}
