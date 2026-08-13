// share.go —— 分享链接基础层:创建 / 列表 / 修改 / 删除(访问解析与下载见 share_access.go)。
//
// 被分享条目按 ItemType 分派到 files/folders 两表,函数按类型返回对应模型对象;
// 文件夹内定位从分享根沿相对路径段逐级下钻(天然不越界)。
// 分享仅支持只读(read,edit 归一为 read);下载计数在真实下载/预览时发生。
package server

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"orbitcloud/common"
	"orbitcloud/core"
	"orbitcloud/log"
	"orbitcloud/model"
)

// generateShareToken 生成唯一短码:8 字节随机 → base62 编码(64 位最多 11 字符)。
func generateShareToken() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate share token: %w", err)
	}
	return new(big.Int).SetBytes(b).Text(62), nil
}

// loadFolderByID 按主键查文件夹(放开桶过滤;供分享解析等无 bucket 上下文的场景)。
func loadFolderByID(ctx context.Context, folderID uint) (*model.Folder, error) {
	var f model.Folder
	if err := core.DB.WithContext(ctx).First(&f, folderID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("load folder by id %d: %w", folderID, err)
	}
	return &f, nil
}

// LoadFolderByIDArg 文件夹按主键加载入参。
type LoadFolderByIDArg struct {
	FolderID uint // 文件夹 folders.id
}

// LoadFolderByID 按主键查文件夹(放开桶过滤;供分享解析/归属预检等无 bucket 上下文的场景)。
func LoadFolderByID(ctx context.Context, arg LoadFolderByIDArg) (*model.Folder, error) {
	return loadFolderByID(ctx, arg.FolderID)
}

// LoadFileByIDArg 文件按主键加载入参。
type LoadFileByIDArg struct {
	FileID uint // 文件 files.id
}

// LoadFileByID 按主键查文件(放开桶过滤;供分享解析/归属预检等无 bucket 上下文的场景)。
func LoadFileByID(ctx context.Context, arg LoadFileByIDArg) (*model.File, error) {
	return loadFileByID(ctx, arg.FileID)
}
// loadFileByID 按主键查文件(放开桶过滤;供分享解析等无 bucket 上下文的场景)。
func loadFileByID(ctx context.Context, fileID uint) (*model.File, error) {
	var f model.File
	if err := core.DB.WithContext(ctx).First(&f, fileID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("load file by id %d: %w", fileID, err)
	}
	return &f, nil
}

// CreateShareArg 创建分享入参(api 层解析请求体)。
type CreateShareArg struct {
	UserID       uint      // 创建者 users.id
	FileID       uint      // 被分享条目(files.id 或 folders.id;server 内部分派 ItemType)
	Permission   string    // read(缺省 read;edit 归一为 read)
	ExpiresAt    time.Time // 过期时间;零值 = 永久
	MaxDownloads int       // 下载次数上限;0 = 不限
	Password     string    // 提取码明文(可选;为空表示无提取码)
}

// CreateShare 创建分享链接(文件或文件夹均可):分派 ItemType 校验条目存在,
// 生成唯一短码,密码 bcrypt 哈希后落库(归属校验由 api 层预检;
// 受限条目也可分享——分享即显式授权通道)。
// 错误语义:条目不存在 → ErrNotFound;参数不合法 → ErrInvalidInput。
func CreateShare(ctx context.Context, arg CreateShareArg) (*model.ShareLink, error) {
	userID := arg.UserID
	// 入参归一
	if arg.FileID == 0 {
		return nil, ErrInvalidInput
	}
	if arg.MaxDownloads < 0 {
		return nil, ErrInvalidInput
	}
	if len(arg.Password) > 32 {
		return nil, ErrInvalidInput // 上限防御
	}
	// Permission 仅支持 read(edit 一律归一为 read)

	// 按类型定位条目:先查 folders 再查 files,命中即确定 ItemType
	itemType := ItemKindFile
	if _, err := loadFolderByID(ctx, arg.FileID); err == nil {
		itemType = ItemKindFolder
	} else if _, err := loadFileByID(ctx, arg.FileID); err == nil {
		itemType = ItemKindFile
	} else {
		return nil, ErrNotFound // 条目不存在(文件夹/文件都未命中)
	}

	// 生成唯一短码(去重循环,最多 5 次)
	var token string
	ok := false
	for attempt := 0; attempt < 5; attempt++ {
		t, err := generateShareToken()
		if err != nil {
			return nil, err
		}
		var cnt int64
		if err := core.DB.WithContext(ctx).Model(&model.ShareLink{}).
			Where("token = ?", t).Count(&cnt).Error; err != nil {
			return nil, fmt.Errorf("create share: check token: %w", err)
		}
		if cnt == 0 {
			token = t
			ok = true
			break
		}
	}
	if !ok {
		return nil, fmt.Errorf("create share: token collision after 5 attempts")
	}

	// 提取码处理(非空 → bcrypt 哈希)
	passwordHash := ""
	if arg.Password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(arg.Password), bcrypt.DefaultCost)
		if err != nil {
			return nil, fmt.Errorf("create share: hash password: %w", err)
		}
		passwordHash = string(hash)
	}

	// 落库(ExpiresAt 零值 = 永久)
	share := &model.ShareLink{
		BucketItemID: arg.FileID,
		ItemType:     itemType,
		CreatorID:    userID,
		Token:        token,
		Permission:   "read", // 归一为只读
		MaxDownloads: arg.MaxDownloads,
		DownloadCount: 0,
		Password:     passwordHash,
	}
	if !arg.ExpiresAt.IsZero() {
		share.ExpiresAt = &arg.ExpiresAt
	}
	if err := core.DB.WithContext(ctx).Create(share).Error; err != nil {
		return nil, fmt.Errorf("create share: %w", err)
	}

	// 返回前清空 Password,不外泄
	share.Password = ""

	log.Infof("create share: user %d item %d (%s) -> token %s", userID, arg.FileID, itemType, share.Token)
	return share, nil
}

// GetShareByTokenArg 分享按短码查询入参。
type GetShareByTokenArg struct {
	Token string // 分享短码
}

// GetShareByToken 按短码查询分享(解析/下载入口)。
// 错误语义:不存在 → ErrNotFound。
func GetShareByToken(ctx context.Context, arg GetShareByTokenArg) (*model.ShareLink, error) {
	token := arg.Token
	if strings.TrimSpace(token) == "" {
		return nil, ErrInvalidInput
	}
	share := &model.ShareLink{}
	err := core.DB.WithContext(ctx).Where("token = ?", token).First(share).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get share by token: %w", err)
	}
	// Password 字段保留:后续校验需要,调用方自行处理
	return share, nil
}

// ListSharesArg 分享列表入参。
type ListSharesArg struct {
	UserID   uint // 创建者 users.id
	Page     int  // 页码(≥1)
	PageSize int  // 页大小(缺省 50,上限 500)
}

// ListShares 分页列出当前用户创建的分享(按 created_at 倒序)。
func ListShares(ctx context.Context, arg ListSharesArg) (total int64, items []model.ShareLink, err error) {
	userID, page, pageSize := arg.UserID, arg.Page, arg.PageSize
	// 分页(统一走 common.Paginate)
	opt := common.NewOption(page, pageSize)
	opt.DefaultOrder = "created_at DESC"

	items = []model.ShareLink{}
	if _, err := common.Paginate(core.DB.WithContext(ctx).Where("creator_id = ?", userID), opt, &items); err != nil {
		return 0, nil, fmt.Errorf("list shares: %w", err)
	}
	if err := core.DB.WithContext(ctx).Model(&model.ShareLink{}).Where("creator_id = ?", userID).Count(&total).Error; err != nil {
		return 0, nil, fmt.Errorf("list shares count: %w", err)
	}

	// 脱敏
	for i := range items {
		items[i].Password = ""
	}
	return total, items, nil
}

// UpdateShareInput 分享可更新字段(指针字段 nil 表示不更新;JSON 绑定走 api 层 DTO)。
type UpdateShareInput struct {
	Permission   *string    // read(edit 归一为 read)
	ExpiresAt    *time.Time // 过期时间(nil 指针 = 不更新;指向零值 = 清除过期,变永久)
	MaxDownloads *int       // 下载次数上限(0 = 不限)
	Password     *string    // 重置提取码(指向空串 = 清除提取码)
}

// LoadShareArg 分享按主键加载入参。
type LoadShareArg struct {
	ShareID uint // 分享 share_links.id
}

// LoadShare 按主键查询分享(可行性:存在;供 api 层归属校验共用)。
// 错误语义:不存在 → ErrNotFound。
func LoadShare(ctx context.Context, arg LoadShareArg) (*model.ShareLink, error) {
	shareID := arg.ShareID
	share := &model.ShareLink{}
	if err := core.DB.WithContext(ctx).First(share, shareID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("load share %d: %w", shareID, err)
	}
	return share, nil
}

// UpdateShareArg 分享更新入参。
type UpdateShareArg struct {
	ShareID uint              // 分享 share_links.id
	In      UpdateShareInput  // 待更新字段(指针字段 nil 表示不更新)
}

// UpdateShare 更新分享(归属校验由 api 层完成,本函数只做可行性)。
// 错误语义:不存在 → ErrNotFound;全空 → ErrInvalidInput。
func UpdateShare(ctx context.Context, arg UpdateShareArg) (*model.ShareLink, error) {
	shareID, in := arg.ShareID, arg.In
	// 查分享(存在性)
	share, err := LoadShare(ctx, LoadShareArg{ShareID: shareID})
	if err != nil {
		return nil, err
	}

	// 构造增量 map
	updates := map[string]any{}
	if in.Permission != nil {
		updates["permission"] = "read" // 非 read 归一为 read
	}
	if in.ExpiresAt != nil {
		if in.ExpiresAt.IsZero() {
			updates["expires_at"] = gorm.Expr("NULL") // 清除过期,变永久
		} else {
			updates["expires_at"] = *in.ExpiresAt
		}
	}
	if in.MaxDownloads != nil {
		if *in.MaxDownloads < 0 {
			return nil, ErrInvalidInput
		}
		updates["max_downloads"] = *in.MaxDownloads
	}
	if in.Password != nil {
		if *in.Password == "" {
			updates["password"] = "" // 清除提取码
		} else {
			hash, err := bcrypt.GenerateFromPassword([]byte(*in.Password), bcrypt.DefaultCost)
			if err != nil {
				return nil, fmt.Errorf("update share: hash password: %w", err)
			}
			updates["password"] = string(hash)
		}
	}
	if len(updates) == 0 {
		return nil, ErrInvalidInput
	}

	// 执行更新
	if err := core.DB.WithContext(ctx).Model(&model.ShareLink{}).Where("id = ?", shareID).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("update share %d: %w", shareID, err)
	}

	// 重查返回(脱敏)
	updated := &model.ShareLink{}
	if err := core.DB.WithContext(ctx).First(updated, shareID).Error; err != nil {
		return nil, err
	}
	updated.Password = ""

	log.Infof("update share: user %d share %d updated", share.CreatorID, shareID)
	return updated, nil
}

// DeleteShareArg 分享删除入参。
type DeleteShareArg struct {
	ShareID uint // 分享 share_links.id
}

// DeleteShare 删除分享(归属校验由 api 层完成,本函数只做可行性)。
// 错误语义:不存在 → ErrNotFound。
func DeleteShare(ctx context.Context, arg DeleteShareArg) error {
	shareID := arg.ShareID
	// 查分享(存在性)
	share, err := LoadShare(ctx, LoadShareArg{ShareID: shareID})
	if err != nil {
		return err
	}

	// 软删(RowsAffected==0 → ErrNotFound)
	res := core.DB.WithContext(ctx).Delete(&model.ShareLink{}, shareID)
	if res.Error != nil {
		return fmt.Errorf("delete share %d: %w", shareID, res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}

	log.Infof("delete share: user %d share %d deleted", share.CreatorID, shareID)
	return nil
}
