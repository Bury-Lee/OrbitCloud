// share_link.go —— 分享链接(对外分享文件/文件夹,可限时/限次/带提取码)。
//
// 被分享条目可能是 files 或 folders 的一行,由 ItemType 区分:
//   - ItemType='file'   → BucketItemID 指向 files.id;
//   - ItemType='folder' → BucketItemID 指向 folders.id。
package model

import (
	"time"

	"gorm.io/gorm"
)

// ShareLink 分享链接。
type ShareLink struct {
	gorm.Model
	BucketItemID  uint       `gorm:"index;not null"`                        // 被分享条目:files.id 或 folders.id(按 ItemType)
	ItemType      string     `gorm:"type:varchar(16);not null;default:file"` // 'file' | 'folder'(双表后新增)
	CreatorID     uint       `gorm:"index;not null"`                        // 发起者 users.id
	Token         string     `gorm:"type:varchar(32);uniqueIndex;not null"` // 分享短码(URL 用)
	Permission    string     `gorm:"type:varchar(16);default:read"`         // read | edit(归一为 read)
	ExpiresAt     *time.Time // 过期时间(nil = 永久)
	MaxDownloads  int        `gorm:"default:0"`        // 下载次数上限(0 = 不限)
	DownloadCount int        `gorm:"default:0"`        // 已下载次数
	Password      string     `gorm:"type:varchar(64)"` // 提取码(存哈希,可选)
}

// TableName share_links。
func (ShareLink) TableName() string { return "share_links" }
