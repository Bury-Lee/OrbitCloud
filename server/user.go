// user.go —— 用户:注册 / 登录 / 刷新 / 登出 / 查询 / 列表 / 修改 / 删除。
//
// 权限模型:0 = 超级管理员(仅命令行添加,API 不可创建);1 = 管理员;
// 其余为普通用户。管理员判定统一为 PermissionLevel.IsAdmin(),数值越小权限越高。
package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"orbitcloud/common"
	"orbitcloud/core"
	"orbitcloud/log"
	"orbitcloud/model"
)

// LoginResult 登录/刷新成功返回的令牌对。
type LoginResult struct {
	AccessToken  string      `json:"access_token"`  // 访问令牌(JWT)
	ExpiresIn    int64       `json:"expires_in"`    // 访问令牌有效期(秒)
	RefreshToken string      `json:"refresh_token"` // 刷新令牌(原始串,仅本次返回)
	User         *model.User `json:"user"`          // 用户信息
}

// RegisterArg 用户注册入参。
type RegisterArg struct {
	Username        string                // 用户名(trim 后非空)
	Password        string                // 密码(≥8 位)
	PermissionLevel model.PermissionLevel // 权限等级(<=0 归一为最低权限普通用户)
}

// Register 注册用户(管理员接口;普通用户不能自助注册):
// trim 用户名 → 校验 → bcrypt 哈希密码 → 写入(Status=1)。
// 权限等级归一:<=0 → 最低权限普通用户;0 仅命令行创建,API 不可授予。
// 错误语义:参数非法 → ErrInvalidInput;用户名已存在 → ErrConflict。
func Register(ctx context.Context, arg RegisterArg) (*model.User, error) {
	username, password, permissionLevel := arg.Username, arg.Password, arg.PermissionLevel
	// 入参校验
	name := strings.TrimSpace(username)
	if name == "" || password == "" || len(password) < 8 {
		return nil, ErrInvalidInput
	}
	// permissionLevel 归一:<=0 → 最低权限普通用户
	perm := permissionLevel
	if perm <= model.SuperAdmin {
		perm = model.NormalUser
	}

	// 密码哈希(bcrypt)
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("register: hash password: %w", err)
	}

	// 构造记录(Name 缺省 = username)
	user := &model.User{
		Username:        name,
		Password:        string(hash),
		Name:            name,
		PermissionLevel: perm,
		Status:          1,
	}

	// 落库(唯一索引兜底并发)
	if err := core.DB.WithContext(ctx).Create(user).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("register: create user: %w", err)
	}

	// 返回前清敏感字段(禁止回传哈希)
	user.Password = ""

	log.Infof("register: user %q (id %d) permission %d created", name, user.ID, perm)
	return user, nil
}

// LoginArg 登录入参。
type LoginArg struct {
	Username string // 用户名
	Password string // 密码明文
}

// Login 登录:查用户 → 校验状态 → 比对密码 → 更新 last_login → 签发令牌对。
// 错误语义:用户不存在或密码错误 → ErrUnauthorized(统一 401,不泄露存在性);账号禁用 → ErrForbidden。
func Login(ctx context.Context, arg LoginArg) (*LoginResult, error) {
	username, password := arg.Username, arg.Password
	// 查用户(不存在统一 401,不泄露存在性)
	var user model.User
	err := core.DB.WithContext(ctx).Where("username = ?", strings.TrimSpace(username)).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, fmt.Errorf("login: query user: %w", err)
	}

	// 状态校验
	if user.Status != 1 {
		return nil, ErrForbidden
	}

	// 密码比对
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
		return nil, ErrUnauthorized
	}

	// 更新 last_login(失败不阻断登录)
	_ = core.DB.Model(&user).Update("last_login", time.Now()).Error

	// 签发令牌对
	result, err := issueTokens(ctx, &user, core.DB)
	if err != nil {
		return nil, err
	}

	log.Infof("login: user %q (id %d) ok", user.Username, user.ID)
	return result, nil
}

// RefreshArg 令牌刷新入参。
type RefreshArg struct {
	RefreshToken string // 刷新令牌(原始串)
}

// Refresh 刷新令牌(JWT 轮换):校验刷新令牌 → 查白名单(未吊销且未过期)→
// 校验用户状态 → 单事务内吊销旧令牌并签发新令牌对。
// 错误语义:令牌无效/过期/已吊销 → ErrUnauthorized;账号禁用 → ErrForbidden。
func Refresh(ctx context.Context, arg RefreshArg) (*LoginResult, error) {
	refreshToken := arg.RefreshToken
	// 入参
	if refreshToken == "" {
		return nil, ErrUnauthorized
	}

	// JWT 校验(自包含 UserID)
	if _, err := core.JWT.VerifyRefresh(refreshToken); err != nil {
		return nil, ErrUnauthorized
	}

	// 按哈希查白名单(未吊销且未过期)
	rt := &model.RefreshToken{}
	err := core.DB.WithContext(ctx).
		Where("token = ? AND revoked_at IS NULL AND expires_at > ?", core.HashToken(refreshToken), time.Now()).
		First(rt).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, fmt.Errorf("refresh: query refresh token: %w", err)
	}

	// 查用户(不存在 → 401)
	user, err := GetUser(ctx, GetUserArg{ID: rt.UserID})
	if err != nil {
		return nil, ErrUnauthorized
	}

	// 状态校验
	if user.Status != 1 {
		return nil, ErrForbidden
	}

	// 吊销旧令牌 + 签发新令牌对(同一事务,保证轮换原子性)
	var result *LoginResult
	err = core.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.RefreshToken{}).Where("id = ?", rt.ID).
			Update("revoked_at", time.Now()).Error; err != nil {
			return err
		}
		res, err := issueTokens(ctx, user, tx)
		if err != nil {
			return err
		}
		result = res
		return nil
	})
	if err != nil {
		log.Errorf("refresh: rotate tokens (user %d, rt %d): %v", rt.UserID, rt.ID, err)
		return nil, fmt.Errorf("refresh: rotate tokens: %w", err)
	}

	log.Infof("refresh: user %d refresh token rotated (old rt %d)", user.ID, rt.ID)
	return result, nil
}

// LogoutArg 登出入参。
type LogoutArg struct {
	RefreshToken string // 刷新令牌(原始串;空 = 幂等)
}

// Logout 登出:吊销刷新令牌(访问令牌依赖短 TTL 自然过期)。
// 参数为空时直接返回 nil(幂等)。
func Logout(ctx context.Context, arg LogoutArg) error {
	refreshToken := arg.RefreshToken
	if refreshToken == "" {
		return nil // 幂等
	}
	// 影响行数 0(令牌不存在/已吊销)也算成功(幂等,不报错)
	err := core.DB.WithContext(ctx).Model(&model.RefreshToken{}).
		Where("token = ? AND revoked_at IS NULL", core.HashToken(refreshToken)).
		Update("revoked_at", time.Now()).Error
	if err != nil {
		return fmt.Errorf("logout: revoke refresh token: %w", err)
	}
	log.Infof("logout: refresh token revoked (hash %s)", core.HashToken(refreshToken))
	return nil
}

// GetUserArg 用户按 ID 查询入参。
type GetUserArg struct {
	ID uint // 用户 users.id
}

// GetUser 按 ID 查询用户。
// 错误语义:不存在 → ErrNotFound。
func GetUser(ctx context.Context, arg GetUserArg) (*model.User, error) {
	id := arg.ID
	var user model.User
	err := core.DB.WithContext(ctx).First(&user, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user %d: %w", id, err)
	}
	user.Password = ""
	return &user, nil
}

// ListUsersArg 用户列表入参。
type ListUsersArg struct {
	Page     int // 页码(≥1)
	PageSize int // 页大小(缺省 50,上限 500)
}

// ListUsers 分页列出用户(按 created_at 倒序;分页统一走 common.Paginate)。
func ListUsers(ctx context.Context, arg ListUsersArg) (total int64, items []model.User, err error) {
	page, pageSize := arg.Page, arg.PageSize
	opt := common.NewOption(page, pageSize)
	opt.DefaultOrder = "created_at DESC"

	items = []model.User{}
	if _, err := common.Paginate(core.DB.WithContext(ctx), opt, &items); err != nil {
		return 0, nil, fmt.Errorf("list users: %w", err)
	}
	if err := core.DB.WithContext(ctx).Model(&model.User{}).Count(&total).Error; err != nil {
		return 0, nil, fmt.Errorf("list users count: %w", err)
	}

	// 脱敏:禁止哈希外泄
	for i := range items {
		items[i].Password = ""
	}
	return total, items, nil
}

// UpdateMeInput 当前用户可更新字段(me 接口专用;不含 PermissionLevel/Status,
// 普通用户不能自提权/自禁)。
type UpdateMeInput struct {
	Password string // 非空则重置密码(bcrypt 哈希后落库)
	Name     string // 名字/昵称
	Email    string // 邮箱
}

// UpdateMeArg 当前用户资料更新入参。
type UpdateMeArg struct {
	UserID uint          // 当前用户 users.id
	In     UpdateMeInput // 待更新字段(仅密码/名字/邮箱)
}

// UpdateMe 修改当前用户(仅密码/名字/邮箱)。
// 错误语义:全部字段为空 → ErrInvalidInput;用户不存在 → ErrNotFound。
func UpdateMe(ctx context.Context, arg UpdateMeArg) (*model.User, error) {
	userID, in := arg.UserID, arg.In
	// 构造增量更新 map(只放非零字段)
	updates := map[string]any{}
	if in.Password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
		if err != nil {
			return nil, fmt.Errorf("update me: hash password: %w", err)
		}
		updates["password"] = string(hash)
	}
	if in.Name != "" {
		updates["name"] = in.Name
	}
	if in.Email != "" {
		updates["email"] = in.Email
	}
	if len(updates) == 0 {
		return nil, ErrInvalidInput
	}

	// 执行更新
	if err := core.DB.WithContext(ctx).Model(&model.User{}).Where("id = ?", userID).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("update me: %w", err)
	}

	// 重查并返回(已脱敏)
	log.Infof("update me: user %d profile updated", userID)
	return GetUser(ctx, GetUserArg{ID: userID})
}

// UpdateUserInput 用户可更新字段(管理员改他人;指针字段 nil 表示不更新;JSON 绑定走 api 层 DTO)。
type UpdateUserInput struct {
	Password        string                // 非空则重置密码(bcrypt 哈希后落库)
	Name            string                // 名字/昵称
	Email           string                // 邮箱
	PermissionLevel *model.PermissionLevel // 权限等级(不能设为 0;0 仅命令行添加)
	Status          *int                  // 1 正常 / 0 禁用
}

// UpdateUserArg 用户更新入参(管理员改他人)。
type UpdateUserArg struct {
	OperatorID uint            // 操作者 users.id(操作日志)
	TargetID   uint            // 目标用户 users.id
	In         UpdateUserInput // 待更新字段(指针字段 nil 表示不更新)
}

// UpdateUser 更新用户(管理员接口,改他人权限/状态;管理员身份与"不能改同级/更高级"
// 由 api 层预检)。错误语义:无更新字段 → ErrInvalidInput;目标不存在 → ErrNotFound。
func UpdateUser(ctx context.Context, arg UpdateUserArg) (*model.User, error) {
	operatorID, targetID, in := arg.OperatorID, arg.TargetID, arg.In
	// 查目标(存在性)
	if _, err := GetUser(ctx, GetUserArg{ID: targetID}); err != nil {
		return nil, err
	}

	// 构造增量更新 map
	updates := map[string]any{}
	if in.Password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
		if err != nil {
			return nil, fmt.Errorf("update user: hash password: %w", err)
		}
		updates["password"] = string(hash)
	}
	if in.Name != "" {
		updates["name"] = in.Name
	}
	if in.Email != "" {
		updates["email"] = in.Email
	}
	if in.PermissionLevel != nil {
		if *in.PermissionLevel < model.Admin {
			return nil, ErrInvalidInput // 0 仅命令行添加;负值非法
		}
		updates["permission_level"] = *in.PermissionLevel
	}
	if in.Status != nil {
		if *in.Status != 0 && *in.Status != 1 {
			return nil, ErrInvalidInput
		}
		updates["status"] = *in.Status
	}
	if len(updates) == 0 {
		return nil, ErrInvalidInput
	}

	// 执行更新
	if err := core.DB.WithContext(ctx).Model(&model.User{}).Where("id = ?", targetID).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("update user %d: %w", targetID, err)
	}

	// 重查并返回(已脱敏)
	log.Infof("update user: operator %d updated user %d", operatorID, targetID)
	return GetUser(ctx, GetUserArg{ID: targetID})
}

// DeleteUserArg 用户删除入参。
type DeleteUserArg struct {
	OperatorID uint // 操作者 users.id(操作日志)
	TargetID   uint // 目标用户 users.id
}

// DeleteUser 删除用户(软删,经 gorm.Model.DeletedAt;管理员接口,
// "同级别不可删"由 api 层预检)。错误语义:目标不存在 → ErrNotFound。
func DeleteUser(ctx context.Context, arg DeleteUserArg) error {
	operatorID, targetID := arg.OperatorID, arg.TargetID
	// 查目标(存在性)
	if _, err := GetUser(ctx, GetUserArg{ID: targetID}); err != nil {
		return err
	}

	// 执行软删
	res := core.DB.WithContext(ctx).Delete(&model.User{}, targetID)
	if res.Error != nil {
		return fmt.Errorf("delete user %d: %w", targetID, res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrNotFound // 不存在或已软删
	}

	// 不级联:桶/文件/分享全部保留,OwnerID 悬空由管理员代管
	log.Infof("delete user: operator %d deleted user %d", operatorID, targetID)
	return nil
}

// VerifyTokenArg 访问令牌校验入参。
type VerifyTokenArg struct {
	Token string // 访问令牌(原始串)
}

// VerifyToken 校验访问令牌并返回 Claims(api 鉴权中间件用;Claims 类型在 core 包)。
func VerifyToken(ctx context.Context, arg VerifyTokenArg) (*core.Claims, error) {
	return core.JWT.Verify(arg.Token)
}

// issueTokens 签发访问+刷新令牌对,并持久化刷新令牌(哈希)到 refresh_tokens。
// db 参数:Login 传 core.DB,Refresh 轮换时传事务句柄 tx(与吊销旧令牌同事务)。
func issueTokens(ctx context.Context, u *model.User, db *gorm.DB) (*LoginResult, error) {
	// 构造访问令牌 Claims 并签发
	c := core.Claims{UserID: u.ID, Username: u.Username, PermissionLevel: u.PermissionLevel}
	accessToken, err := core.JWT.SignAccess(c)
	if err != nil {
		return nil, err
	}

	// 签发刷新令牌(JWT,自包含 UserID)
	rawRefresh, err := core.JWT.SignRefresh(core.RefreshClaim{UserID: u.ID})
	if err != nil {
		return nil, err
	}

	// 刷新令牌落库(只存哈希,防重放靠唯一索引)
	rt := &model.RefreshToken{
		UserID:    u.ID,
		Token:     core.HashToken(rawRefresh),
		ExpiresAt: time.Now().Add(core.JWT.RefreshTTL),
	}
	if err := db.WithContext(ctx).Create(rt).Error; err != nil {
		return nil, fmt.Errorf("issue tokens: save refresh token: %w", err) // 不返回半套令牌
	}

	// 组装返回(明文刷新令牌仅本次返回)
	return &LoginResult{
		AccessToken:  accessToken,
		ExpiresIn:    int64(core.JWT.AccessTTL.Seconds()),
		RefreshToken: rawRefresh,
		User:         sanitizeUser(u),
	}, nil
}

// 用户维度访问判定已迁移至 api/perm.go:canAccess。

// sanitizeUser 返回脱敏用户副本(Password 置空),禁止内部对象外泄哈希。
func sanitizeUser(u *model.User) *model.User {
	if u == nil {
		return nil
	}
	copy := *u
	copy.Password = ""
	return &copy
}
