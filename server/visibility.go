// visibility.go —— 条目级可见性(用户组 ACL)。
//
// File/Folder.VisibleToGroups 存"可见组 ID"的 JSON 数组(如 "[1,5]"):
// 空串 = 不限制(按桶级权限可见);非空 = 仅创建者 / 管理员(权限 <= 1)/
// 可见组内成员可访问。组为纯白名单参考,无权限等级概念。
//
// 权限判定已迁至 api/perm.go;本文件保留可见性写路径可行性(条目/组存在)
// 与列表过滤型查询谓词(listVisibilityPredicate / visibilitySQL,数据塑形)。
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"orbitcloud/core"
	"orbitcloud/log"
	"orbitcloud/model"
)

// marshalVisibleGroups 组 ID 列表 → JSON 字符串(空列表 → 空串,表示不限制)。
func marshalVisibleGroups(ids []uint) (string, error) {
	if len(ids) == 0 {
		return "", nil
	}
	b, err := json.Marshal(ids)
	if err != nil {
		return "", fmt.Errorf("marshal visible groups: %w", err)
	}
	return string(b), nil
}

// checkAncestorsAccessTree 沿祖先链逐层校验目录可用性与条目级可见性
// (语义同 api.permAncestorsAccess;目标节点由 server 内部解析/遍历,api 层无法预检)。
// 一次遍历同时完成 Isable 与 ACL 判定,用户/组成员数据仅加载一次;桶根直接通过。
func checkAncestorsAccessTree(ctx context.Context, userID, bucketID, folderID uint) error {
	var user *model.User
	var userGroups []uint
	cur := folderID
	seen := map[uint]bool{} // 环保护(数据异常时防死循环)
	for cur != 0 && !seen[cur] {
		seen[cur] = true
		f, err := loadFolder(ctx, bucketID, cur)
		if err != nil {
			return err
		}
		if !f.Isable {
			return ErrNotFound // 目录已删 → 不可达(原 checkAncestorsUsable 语义)
		}
		if user == nil { // 用户/组成员仅加载一次(链上各层复用)
			u, err := GetUser(ctx, GetUserArg{ID: userID})
			if err != nil {
				return err
			}
			user = u
		}
		if err := checkItemAccessTree(ctx, user, &userGroups, f.VisibleToGroups, f.UploadedBy); err != nil {
			return err
		}
		cur = f.ParentID
	}
	return nil
}

// checkItemAccessTree 条目级可见性判定(供树过滤用,规则统一走 ItemVisibleRule;
// user 为链层已加载用户,groups 为跨层共享的组成员缓存)。
func checkItemAccessTree(ctx context.Context, user *model.User, groups *[]uint, visibleToGroups string, uploadedBy uint) error {
	if strings.TrimSpace(visibleToGroups) == "" {
		return nil // 空 = 不限制
	}
	if uploadedBy == user.ID {
		return nil
	}
	if user.PermissionLevel <= 1 {
		return nil
	}
	if *groups == nil {
		gs, err := UserGroupIDs(ctx, UserGroupIDsArg{UserID: user.ID})
		if err != nil {
			return err
		}
		*groups = gs
	}
	return ItemVisibleRule(user.ID, visibleToGroups, uploadedBy, user, *groups)
}

// ParseVisibleGroups 解析 VisibleToGroups 的 JSON 数组为组 ID 列表(去重、剔 0;
// api 层权限原语与 server 层树过滤共用)。
func ParseVisibleGroups(s string) ([]uint, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	ids := []uint{}
	if err := json.Unmarshal([]byte(s), &ids); err != nil {
		return nil, fmt.Errorf("parse visible groups: %w", err)
	}
	seen := map[uint]bool{}
	out := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

// ItemVisibleRule 条目级可见性纯判定(单一实现,api/server 共用):
// 空 visibleToGroups 不限制;非空时仅创建者 / 管理员(权限<=1)/ 可见组内成员可访问,
// 解析失败或空数组 → ErrForbidden(数据异常拒绝访问)。
// user 与 userGroups 由调用方加载,本函数不查库。
func ItemVisibleRule(userID uint, visibleToGroups string, uploadedBy uint, user *model.User, userGroups []uint) error {
	if strings.TrimSpace(visibleToGroups) == "" {
		return nil // 空 = 不限制
	}
	groupIDs, err := ParseVisibleGroups(visibleToGroups)
	if err != nil {
		return ErrForbidden // 数据异常:拒绝访问(防越权),不泄露内部
	}
	if len(groupIDs) == 0 {
		return ErrForbidden
	}
	if uploadedBy == userID {
		return nil
	}
	if user == nil || user.PermissionLevel <= 1 {
		if user == nil {
			return ErrForbidden // 调用方未加载用户(不应发生);拒绝而非 panic
		}
		return nil // 管理员可见一切(含受限条目)
	}
	for _, gid := range userGroups {
		for _, vgid := range groupIDs {
			if gid == vgid {
				return nil
			}
		}
	}
	return ErrForbidden
}

// SetFileVisibilityArg 文件可见组设置入参。
type SetFileVisibilityArg struct {
	UserID   uint   // 操作者 users.id(操作日志)
	BucketID uint   // 所属桶
	FileID   uint   // 文件 files.id
	GroupIDs []uint // 可见组 ID 列表(空 = 恢复不限制)
}

// SetFileVisibility 设置文件可见组(权限预检在 api 层)。
// groupIDs 空 = 恢复不限制;组不存在/已禁用 → ErrInvalidInput。
func SetFileVisibility(ctx context.Context, arg SetFileVisibilityArg) error {
	return setItemVisibility(ctx, arg.UserID, arg.BucketID, ItemKindFile, arg.FileID, arg.GroupIDs)
}

// SetFolderVisibilityArg 文件夹可见组设置入参。
type SetFolderVisibilityArg struct {
	UserID   uint   // 操作者 users.id(操作日志)
	BucketID uint   // 所属桶
	FolderID uint   // 文件夹 folders.id
	GroupIDs []uint // 可见组 ID 列表(空 = 恢复不限制)
}

// SetFolderVisibility 设置文件夹可见组:仅影响本目录行,不递归子树。
func SetFolderVisibility(ctx context.Context, arg SetFolderVisibilityArg) error {
	return setItemVisibility(ctx, arg.UserID, arg.BucketID, ItemKindFolder, arg.FolderID, arg.GroupIDs)
}

// setItemVisibility 通用可见组设置:校验条目与目标组存在后写入(权限预检在 api 层)。
func setItemVisibility(ctx context.Context, userID, bucketID uint, itemType string, itemID uint, groupIDs []uint) error {
	// 查条目(存在 + 属于该桶)
	switch itemType {
	case ItemKindFile:
		if _, err := loadFile(ctx, bucketID, itemID); err != nil {
			return err
		}
	case ItemKindFolder:
		if _, err := loadFolder(ctx, bucketID, itemID); err != nil {
			return err
		}
	default:
		return ErrInvalidInput
	}

	// 校验目标组存在且正常(去重,忽略 0)
	ids, err := normalizeAndCheckGroups(ctx, groupIDs)
	if err != nil {
		return err
	}

	// 写 JSON(空列表 → 空串 = 不限制)
	jsonStr, err := marshalVisibleGroups(ids)
	if err != nil {
		return err
	}
	switch itemType {
	case ItemKindFile:
		if err := core.DB.WithContext(ctx).Model(&model.File{}).
			Where("id = ? AND bucket_id = ?", itemID, bucketID).
			Update("visible_to_groups", jsonStr).Error; err != nil {
			return fmt.Errorf("set file %d visibility: %w", itemID, err)
		}
	case ItemKindFolder:
		if err := core.DB.WithContext(ctx).Model(&model.Folder{}).
			Where("id = ? AND bucket_id = ?", itemID, bucketID).
			Update("visible_to_groups", jsonStr).Error; err != nil {
			return fmt.Errorf("set folder %d visibility: %w", itemID, err)
		}
	}

	log.Infof("visibility: user %d set %s %d (bucket %d) visible to groups %v", userID, itemType, itemID, bucketID, ids)
	return nil
}

// normalizeAndCheckGroups 归一化组 ID 列表(去重/剔 0)并校验全部组存在且状态正常。
// 错误语义:任一组不存在或已禁用 → ErrInvalidInput(整体拒绝,避免部分生效)。
func normalizeAndCheckGroups(ctx context.Context, groupIDs []uint) ([]uint, error) {
	seen := map[uint]bool{}
	ids := make([]uint, 0, len(groupIDs))
	for _, id := range groupIDs {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return ids, nil // 空 = 恢复不限制
	}
	var count int64
	if err := core.DB.WithContext(ctx).Model(&model.UserGroup{}).
		Where("id IN ? AND status = 1", ids).Count(&count).Error; err != nil {
		return nil, fmt.Errorf("check visible groups: %w", err)
	}
	if count != int64(len(ids)) {
		return nil, ErrInvalidInput // 有组不存在/被禁用
	}
	return ids, nil
}

// listVisibilityPredicate 构造条目级可见性过滤谓词:管理员返回空(不过滤),
// 其余返回 SQL 片段与参数。
func listVisibilityPredicate(ctx context.Context, userID uint) (string, []any, error) {
	user, err := userForVisibility(ctx, userID)
	if err != nil {
		return "", nil, err
	}
	groups, err := UserGroupIDs(ctx, UserGroupIDsArg{UserID: userID})
	if err != nil {
		return "", nil, err
	}
	sql, args := visibilitySQL(core.DB.Dialector.Name(), user, groups)
	return sql, args, nil
}

// visibilitySQL 构造条目级可见性过滤 SQL 片段:管理员或未受限或创建者或
// 可见组内成员 → 通过;dialect 感知(SQLite json_each / PostgreSQL json_array_elements_text)。
func visibilitySQL(dialect string, user *model.User, userGroupIDs []uint) (string, []any) {
	if user == nil || user.PermissionLevel <= 1 {
		return "", nil // 管理员不过滤
	}
	// 拼接组匹配子句(IN 列表)
	var groupClause string
	if len(userGroupIDs) > 0 {
		switch dialect {
		case "sqlite":
			placeholders := strings.TrimSuffix(strings.Repeat("?,", len(userGroupIDs)), ",")
			groupClause = fmt.Sprintf(" OR EXISTS (SELECT 1 FROM json_each(visible_to_groups) WHERE CAST(json_each.value AS INTEGER) IN (%s))", placeholders)
		default: // postgres / postgresql
			placeholders := strings.TrimSuffix(strings.Repeat("?,", len(userGroupIDs)), ",")
			groupClause = fmt.Sprintf(" OR EXISTS (SELECT 1 FROM json_array_elements_text(visible_to_groups::json) WHERE CAST(value AS INTEGER) IN (%s))", placeholders)
		}
	}
	// 参数顺序:uploaded_by, 组 ID 列表...
	args := make([]any, 0, 1+len(userGroupIDs))
	args = append(args, user.ID)
	for _, gid := range userGroupIDs {
		args = append(args, gid)
	}
	sql := "(visible_to_groups = '' OR visible_to_groups IS NULL OR uploaded_by = ?" + groupClause + ")"
	return sql, args
}

// userForVisibility 加载用户供可见性过滤/校验(仅取数据)。
func userForVisibility(ctx context.Context, userID uint) (*model.User, error) {
	var user model.User
	err := core.DB.WithContext(ctx).First(&user, userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load user %d: %w", userID, err)
	}
	return &user, nil
}
