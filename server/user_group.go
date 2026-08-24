// user_group.go —— 用户组:组 CRUD / 成员管理 / 我的组 / 组成员 ID 查询。
//
// 用户组支撑文件/文件夹"仅组 n 可见"(条目级可见性,见 visibility.go)。
// 管理员(IsAdmin)可管理组与成员,组内成员可查看组信息;
// 组删除采用软删;组无权限等级概念(纯可见组白名单,不参与等级体系判定)。
package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"orbitcloud/common"
	"orbitcloud/core"
	"orbitcloud/log"
	"orbitcloud/model"
)

// CreateGroupArg 用户组创建入参。
type CreateGroupArg struct {
	OperatorID  uint   // 操作者 users.id(操作日志)
	Name        string // 组名(trim 后非空;全局唯一,大小写敏感)
	Description string // 描述
}

// CreateGroup 创建用户组(管理员接口):trim 组名 → 校验非空 → 落库。
// 错误语义:组名为空 → ErrInvalidInput;组名已存在 → ErrConflict。
func CreateGroup(ctx context.Context, arg CreateGroupArg) (*model.UserGroup, error) {
	operatorID, name, description := arg.OperatorID, arg.Name, arg.Description
	// 入参校验与归一
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrInvalidInput
	}

	// 落库(唯一索引兜底重名)
	group := &model.UserGroup{
		Name:        name,
		Description: description,
		CreatedBy:   operatorID,
		Status:      1,
	}
	if err := core.DB.WithContext(ctx).Create(group).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("create group: %w", err)
	}

	log.Infof("group: user %d created group %q (id %d)", operatorID, name, group.ID)
	return group, nil
}

// GetGroupArg 用户组按 ID 查询入参。
type GetGroupArg struct {
	GroupID uint // 用户组 user_groups.id
}

// GetGroup 按 ID 查询用户组(可行性:组存在;可见性权限由 api 层 permGroupVisible 预检)。
// 错误语义:不存在 → ErrNotFound。
func GetGroup(ctx context.Context, arg GetGroupArg) (*model.UserGroup, error) {
	return loadGroup(ctx, arg.GroupID)
}

// ListGroupsArg 用户组列表入参。
type ListGroupsArg struct {
	UserID   uint // 操作者(管理员看全部;普通用户仅看自己所在组)
	Page     int  // 页码(≥1)
	PageSize int  // 页大小(缺省 50,上限 500)
}

// ListGroups 分页列出用户组:管理员看全部,普通用户仅看自己所在组。
func ListGroups(ctx context.Context, arg ListGroupsArg) (total int64, items []model.UserGroup, err error) {
	userID, page, pageSize := arg.UserID, arg.Page, arg.PageSize
	// 管理员不过滤;普通用户限定自己所在组
	user, err := GetUser(ctx, GetUserArg{ID: userID})
	if err != nil {
		return 0, nil, err
	}
	db := core.DB.WithContext(ctx)
	opt := common.NewOption(page, pageSize)
	opt.DefaultOrder = "created_at DESC"
	if !user.PermissionLevel.IsAdmin() {
		ids, err := UserGroupIDs(ctx, UserGroupIDsArg{UserID: userID})
		if err != nil {
			return 0, nil, err
		}
		if len(ids) == 0 {
			return 0, []model.UserGroup{}, nil // 无组可看,直接空页
		}
		db = db.Where("id IN ?", ids)
	}

	// 分页查询 + 总数(同一过滤谓词)
	items = []model.UserGroup{}
	if _, err := common.Paginate(db, opt, &items); err != nil {
		return 0, nil, fmt.Errorf("list groups: %w", err)
	}
	if err := db.Model(&model.UserGroup{}).Count(&total).Error; err != nil {
		return 0, nil, fmt.Errorf("list groups count: %w", err)
	}
	return total, items, nil
}

// UpdateGroupArg 用户组更新入参。
type UpdateGroupArg struct {
	OperatorID  uint   // 操作者 users.id(操作日志)
	GroupID     uint   // 目标组 user_groups.id
	Name        string // 新组名(空 = 不更新)
	Description string // 新描述(空 = 不更新)
	Status      *int8  // 新状态(0/1;nil = 不更新)
}

// UpdateGroup 更新用户组(管理员接口;空字段不更新,Status 仅允许 0/1)。
// 错误语义:无更新字段 → ErrInvalidInput;组不存在 → ErrNotFound。
func UpdateGroup(ctx context.Context, arg UpdateGroupArg) (*model.UserGroup, error) {
	operatorID, groupID, name, description, status := arg.OperatorID, arg.GroupID, arg.Name, arg.Description, arg.Status
	// 查组(存在性)
	if _, err := loadGroup(ctx, groupID); err != nil {
		return nil, err
	}

	// 构造增量更新 map(只放非零字段)
	updates := map[string]any{}
	if name != "" {
		updates["name"] = strings.TrimSpace(name)
	}
	if description != "" {
		updates["description"] = description
	}
	if status != nil {
		if *status != 0 && *status != 1 {
			return nil, ErrInvalidInput
		}
		updates["status"] = *status
	}
	if len(updates) == 0 {
		return nil, ErrInvalidInput
	}

	// 执行更新(重名 → 冲突)
	if err := core.DB.WithContext(ctx).Model(&model.UserGroup{}).Where("id = ?", groupID).Updates(updates).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("update group %d: %w", groupID, err)
	}

	log.Infof("group: user %d updated group %d", operatorID, groupID)
	return loadGroup(ctx, groupID)
}

// DeleteGroupArg 用户组删除入参。
type DeleteGroupArg struct {
	OperatorID uint // 操作者 users.id(操作日志)
	GroupID    uint // 目标组 user_groups.id
}

// DeleteGroup 删除用户组(软删,管理员接口):成员关系随组软删自动不可用,
// 条目上遗留的组 ID 引用按现存组匹配,组不存在即视为无此组。
// 错误语义:组不存在 → ErrNotFound。
func DeleteGroup(ctx context.Context, arg DeleteGroupArg) error {
	operatorID, groupID := arg.OperatorID, arg.GroupID
	// 查组 + 软删(不级联成员表,成员行留着无害)
	if _, err := loadGroup(ctx, groupID); err != nil {
		return err
	}
	res := core.DB.WithContext(ctx).Delete(&model.UserGroup{}, groupID)
	if res.Error != nil {
		return fmt.Errorf("delete group %d: %w", groupID, res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}

	log.Infof("group: user %d deleted group %d", operatorID, groupID)
	return nil
}

// AddGroupMemberArg 添加组成员入参。
type AddGroupMemberArg struct {
	OperatorID uint // 操作者 users.id(操作日志)
	GroupID    uint // 目标组 user_groups.id
	UserID     uint // 加入成员 users.id
}

// AddGroupMember 添加成员到用户组(管理员接口;管理员校验由 api 层 AdminMiddleware 完成)。
// 错误语义:组不存在/已禁用 → ErrNotFound;用户不存在 → ErrNotFound;已在组内 → ErrConflict。
func AddGroupMember(ctx context.Context, arg AddGroupMemberArg) error {
	operatorID, groupID, userID := arg.OperatorID, arg.GroupID, arg.UserID
	// 组存在且正常
	group, err := loadGroup(ctx, groupID)
	if err != nil {
		return err
	}
	if group.Status != 1 {
		return ErrNotFound // 已禁用组不可加人(404 防探测)
	}

	// 用户存在(软删用户不可加)
	if _, err := GetUser(ctx, GetUserArg{ID: userID}); err != nil {
		return err
	}

	// 落库(联合唯一索引兜底重复)
	member := &model.UserGroupMember{GroupID: groupID, UserID: userID}
	if err := core.DB.WithContext(ctx).Create(member).Error; err != nil {
		if isUniqueViolation(err) {
			return ErrConflict
		}
		return fmt.Errorf("add group member: %w", err)
	}

	log.Infof("group: user %d added user %d to group %d", operatorID, userID, groupID)
	return nil
}

// RemoveGroupMemberArg 移除组成员入参。
type RemoveGroupMemberArg struct {
	OperatorID uint // 操作者 users.id(操作日志)
	GroupID    uint // 目标组 user_groups.id
	UserID     uint // 移除成员 users.id
}

// RemoveGroupMember 从用户组移除成员(管理员接口;管理员校验由 api 层 AdminMiddleware 完成)。
// 错误语义:组不存在 → ErrNotFound;成员不存在于该组 → ErrNotFound。
func RemoveGroupMember(ctx context.Context, arg RemoveGroupMemberArg) error {
	operatorID, groupID, userID := arg.OperatorID, arg.GroupID, arg.UserID
	// 组存在
	if _, err := loadGroup(ctx, groupID); err != nil {
		return err
	}

	// 移除成员(硬删中间表行;不存在 → ErrNotFound)
	res := core.DB.WithContext(ctx).
		Where("group_id = ? AND user_id = ?", groupID, userID).
		Delete(&model.UserGroupMember{})
	if res.Error != nil {
		return fmt.Errorf("remove group member: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}

	log.Infof("group: user %d removed user %d from group %d", operatorID, userID, groupID)
	return nil
}

// MemberInfo 成员视图(成员行 + 用户基本信息,已脱敏)。
type MemberInfo struct {
	model.UserGroupMember
	Username string `json:"username"`
	Name     string `json:"name"`
}

// ListGroupMembersArg 组成员列表入参。
type ListGroupMembersArg struct {
	GroupID  uint // 目标组 user_groups.id
	Page     int  // 页码(≥1)
	PageSize int  // 页大小(缺省 50,上限 500)
}

// ListGroupMembers 分页列出组内成员(组存在性由本函数校验,可见性由 api 层预检)。
func ListGroupMembers(ctx context.Context, arg ListGroupMembersArg) (total int64, items []MemberInfo, err error) {
	groupID, page, pageSize := arg.GroupID, arg.Page, arg.PageSize
	// 组存在
	if _, err := loadGroup(ctx, groupID); err != nil {
		return 0, nil, err
	}

	// 分页查询成员行(按创建时间倒序)
	members := []model.UserGroupMember{}
	opt := common.NewOption(page, pageSize)
	opt.DefaultOrder = "created_at DESC"
	if _, err := common.Paginate(core.DB.WithContext(ctx).Where("group_id = ?", groupID), opt, &members); err != nil {
		return 0, nil, fmt.Errorf("list group members: %w", err)
	}
	if err := core.DB.WithContext(ctx).Model(&model.UserGroupMember{}).
		Where("group_id = ?", groupID).Count(&total).Error; err != nil {
		return 0, nil, fmt.Errorf("list group members count: %w", err)
	}

	// 批量补用户信息(IN 一次取全;软删用户自动跳过)
	items = make([]MemberInfo, 0, len(members))
	if len(members) > 0 {
		ids := make([]uint, 0, len(members))
		for _, m := range members {
			ids = append(ids, m.UserID)
		}
		var users []model.User
		if err := core.DB.WithContext(ctx).Where("id IN ?", ids).Find(&users).Error; err != nil {
			return 0, nil, fmt.Errorf("list group members: load users: %w", err)
		}
		byID := map[uint]model.User{}
		for i := range users {
			users[i].Password = "" // 脱敏
			byID[users[i].ID] = users[i]
		}
		for _, m := range members {
			user, ok := byID[m.UserID]
			if !ok {
				continue // 用户已软删,跳过
			}
			items = append(items, MemberInfo{UserGroupMember: m, Username: user.Username, Name: user.Name})
		}
	}
	return total, items, nil
}

// ListMyGroupsArg 我的组列表入参。
type ListMyGroupsArg struct {
	UserID uint // 当前用户 users.id
}

// ListMyGroups 查询当前用户所属的全部组(仅正常组)。
func ListMyGroups(ctx context.Context, arg ListMyGroupsArg) ([]model.UserGroup, error) {
	userID := arg.UserID
	groupIDs, err := UserGroupIDs(ctx, UserGroupIDsArg{UserID: userID})
	if err != nil {
		return nil, err
	}
	groups := []model.UserGroup{}
	if len(groupIDs) == 0 {
		return groups, nil
	}
	if err := core.DB.WithContext(ctx).
		Where("id IN ? AND status = 1", groupIDs).
		Order("created_at ASC").Find(&groups).Error; err != nil {
		return nil, fmt.Errorf("list my groups: %w", err)
	}
	return groups, nil
}

// loadGroup 按 ID 加载用户组(常规查询排除软删)。
func loadGroup(ctx context.Context, groupID uint) (*model.UserGroup, error) {
	var group model.UserGroup
	err := core.DB.WithContext(ctx).First(&group, groupID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load group %d: %w", groupID, err)
	}
	return &group, nil
}

// 组可见性判定已迁移至 api.permGroupVisible。

// UserGroupIDsArg 用户所属组 ID 查询入参。
type UserGroupIDsArg struct {
	UserID uint // 用户 users.id
}

// UserGroupIDs 返回用户所属组的 ID 列表(供 api 层 ACL 与 server 层列表过滤共用)。
func UserGroupIDs(ctx context.Context, arg UserGroupIDsArg) ([]uint, error) {
	userID := arg.UserID
	rows := []model.UserGroupMember{}
	if err := core.DB.WithContext(ctx).
		Select("group_id").Where("user_id = ?", userID).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("user group ids: %w", err)
	}
	ids := make([]uint, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.GroupID)
	}
	return ids, nil
}
