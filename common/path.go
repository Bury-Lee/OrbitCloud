// path.go —— 路径 → 目录节点定位。
//
// 核心语义:
//   - 桶根 = 虚拟(桶名即根,无实例行):ResolveDirPath 返回 0 表示桶根;
//   - 路径段逐级解析:第一段查 parent_id=0(桶根),后续段查 parent_id=上一段 id;
//   - mkdir -p:不存在的段自动创建(幂等),占用冲突由 uk_folder 唯一索引兜底;
//   - 链长限制:完整路径长度 ≤ 512,超长 → ErrInvalidInput;
//   - 本函数是"按路径定位"的唯一入口(上传/建目录/移动/分享共用)。
package common

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"

	"orbitcloud/core"
	"orbitcloud/model"
)

// MaxDirPathLen 完整目录路径长度上限(整条路径多段拼接的总长上限)。
const MaxDirPathLen = 512

// ResolveDirPath 把规范化目录路径(如 "b/c/d" 或 "/")解析为 folder ID;mkdir -p 语义。
// 返回:桶根 "/" → 0(虚拟桶根,无实例);否则 → 链末端 folder.ID(不存在的段自动创建)。
// 错误语义:路径超长 → ErrInvalidInput;段被并发占用(唯一索引冲突)→ ErrConflict。
func ResolveDirPath(ctx context.Context, userID, bucketID uint, dirPath string) (folderID uint, err error) {
	if utf8.RuneCountInString(dirPath) > MaxDirPathLen {
		return 0, ErrInvalidInput
	}

	parentID := uint(0)
	chain := strings.Split(dirPath, "/")
	// 段数守卫(防御性):总长已限制 ≤512,段数最多约 256,本守卫实际不可达
	if len(chain) > maxDirPathLen {
		return 0, errors.New("too longer")
	}
	for _, seg := range chain {
		if seg == "" {
			continue // 桶根 "/" 或重复斜杠 → 跳过,保持 parentID=0
		}
		// 与文件名同一套校验(拒 CON/PRN/AUX/NUL、`:*?"<>|`、结尾点空格、控制字符),
		// 校验放查询之前,非法段名一律 400
		if err := ValidateItemName(seg); err != nil {
			return 0, err
		}
		var f model.Folder
		err := core.DB.WithContext(ctx).
			Where("bucket_id = ? AND parent_id = ? AND name_lower = ?",
				bucketID, parentID, strings.ToLower(seg)).
			First(&f).Error
		if err == nil {
			parentID = f.ID // 段已存在,继续下一段
			continue
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 段不存在 → 创建(mkdir -p 语义;无实体对象,只落 folders 行)
			f = model.Folder{
				BucketID:   bucketID,
				ParentID:   parentID,
				Name:       seg,
				NameLower:  strings.ToLower(seg),
				UploadedBy: userID,
				Isable:     true,
			}
			if err := core.DB.WithContext(ctx).Create(&f).Error; err != nil {
				if errors.Is(err, gorm.ErrDuplicatedKey) {
					return 0, ErrConflict // 并发下被占,由唯一索引兜底拦截
				}
				return 0, err
			}
			parentID = f.ID
		} else {
			return 0, err
		}
	}
	return parentID, nil
}

// ResolveDirPathStrict 把规范化目录路径解析为 folder ID;纯解析,不建链。
// 与 ResolveDirPath 的差异:路径段不存在 → 直接返回 ErrNotFound(不自动创建)。
// 用途:只读路径(浏览列表)不允许"浏览即创建"——目录被删除后,同路径浏览应返回
// 404,而不是 mkdir -p 幽灵重建(删除"复活")。
func ResolveDirPathStrict(ctx context.Context, bucketID uint, dirPath string) (folderID uint, err error) {
	if utf8.RuneCountInString(dirPath) > MaxDirPathLen {
		return 0, ErrInvalidInput
	}

	parentID := uint(0)
	chain := strings.Split(dirPath, "/")
	if len(chain) > maxDirPathLen {
		return 0, errors.New("too longer")
	}
	for _, seg := range chain {
		if seg == "" {
			continue // 桶根 "/" 或重复斜杠 → 跳过,保持 parentID=0
		}
		if err := ValidateItemName(seg); err != nil {
			return 0, err
		}
		var f model.Folder
		err := core.DB.WithContext(ctx).
			Where("bucket_id = ? AND parent_id = ? AND name_lower = ?",
				bucketID, parentID, strings.ToLower(seg)).
			First(&f).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, ErrNotFound // 段不存在 → 目录不存在,拒绝建链
		}
		if err != nil {
			return 0, err
		}
		parentID = f.ID
	}
	return parentID, nil
}
