// recycle_bin_step.go —— 升级步骤 1(回收站软删配套):
// files/folders 的唯一名索引改部分索引(WHERE deleted_at IS NULL)。
//
// 背景:gorm.Model.DeletedAt 软删后行仍保留,若唯一索引仍覆盖全部行,
// 回收站内旧记录会挡住同名重新上传/建目录/改名(违反唯一约束);
// 部分索引让回收站软删行与活动行同名共存,活动行之间仍保持唯一(并发创建兜底)。
// 语句全部幂等(IF EXISTS / IF NOT EXISTS),失败可安全重试;
// sqlite 与 postgres 均支持部分唯一索引。
package migrate

import "gorm.io/gorm"

// applyRecycleBinIndexes 步骤 1 实现。
func applyRecycleBinIndexes(db *gorm.DB) error {
	stmts := []string{
		// files:同文件夹内 (bucket_id, folder_id, name_lower) 唯一(活动行)
		"DROP INDEX IF EXISTS uk_file_lower",
		"CREATE UNIQUE INDEX IF NOT EXISTS uk_file_lower ON files (bucket_id, folder_id, name_lower) WHERE deleted_at IS NULL",
		// folders:同父目录下 (bucket_id, parent_id, name_lower) 唯一(活动行)
		"DROP INDEX IF EXISTS uk_folder_lower",
		"CREATE UNIQUE INDEX IF NOT EXISTS uk_folder_lower ON folders (bucket_id, parent_id, name_lower) WHERE deleted_at IS NULL",
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			return err
		}
	}
	return nil
}
