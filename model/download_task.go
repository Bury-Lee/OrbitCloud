// download_task.go —— 下载任务表(文件夹下载登记)。
// 语义:
//   - 客户端对文件夹发起下载时 POST 登记任务(记录起点文件夹与路径快照);
//     单文件下载直接走原生下载接口,不登记任务;
//   - 下载执行由前端主导(分层拉取 + 并发逐文件下载,进度/断点缓存于前端),
//     本表仅作轻量登记,防重入与审计;
//   - 完成/取消后 DELETE 硬删任务行(不保留历史)。
//
// 归属:任务仅创建者本人可查/改/删(api 层按 UserID 过滤)。
package model

import "gorm.io/gorm"

// DownloadTask 文件夹下载任务。
type DownloadTask struct {
	gorm.Model
	UserID   uint   `gorm:"index;not null"`    // 创建者 users.id(断点信息归属,防他人读取)
	BucketID uint   `gorm:"index;not null"`    // 下载起点文件夹所属桶 buckets.id
	FolderID uint   `gorm:"index;not null"`    // 下载起点文件夹 folders.id(文件夹下载登记)
	FilePath string `gorm:"type:varchar(512)"` // 路径快照(folderIDToPath 派生,仅供展示,可能过期)
}

// TableName download_tasks。
func (DownloadTask) TableName() string { return "download_tasks" }
