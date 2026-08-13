package model

import (
	"time"

	"gorm.io/gorm"
)

// RefreshToken 登录刷新令牌(配合刷新令牌轮换;签发见 core/jwt.go SignRefresh)。
type RefreshToken struct {
	gorm.Model
	UserID    uint       `gorm:"index;not null"`                         // 所属用户 users.id
	Token     string     `gorm:"type:varchar(255);uniqueIndex;not null"` // 刷新令牌的 SHA-256 哈希(服务端只存哈希,原始串仅签发时返回)
	ExpiresAt time.Time  `gorm:"index;not null"`                         // 过期时间
	RevokedAt *time.Time // 吊销时间(登出 / 令牌轮换后置位)
	UserAgent string     `gorm:"type:varchar(255)"` // 设备标识(可选)
	IP        string     `gorm:"type:varchar(45)"`  // 签发 IP(可选)
}

// TableName refresh_tokens。
func (RefreshToken) TableName() string { return "refresh_tokens" }
