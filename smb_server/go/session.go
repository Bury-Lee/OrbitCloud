// session.go —— 操作身份注册表与权限校验(SMB 网关专用)。
//
// 身份机制(设计点:P0-1/P0-2 修复):
//   - Rust 侧 SMB 会话认证成功后,凭据查询/快照携带 users.id(UserCred.ID),
//     会话即与该 UserID 绑定;每个文件操作帧随请求携带 userId;
//   - 本注册表用 ristretto(为大量 key 优化的内存缓存库,零 GC 压力,
//     后续文件缓存可复用同一实例)缓存 UserID → 用户信息,TTL 60s 自然
//     过期重载 —— 即"每 60 秒更新一次"(与 WatchAndPush 对账周期对齐);
//   - 缓存 miss 时实时查库兜底,避免新注册用户不可用。
//
// 权限校验与 HTTP 通道同口径(对应 api/perm.go 的 precheck* 系列):
//   - 读操作:桶可用 + 桶权限等级 + 祖先链(Isable/可见组)+ 条目可见组;
//   - 写/改操作:桶可用 + 桶权限等级 + 目标目录祖先链可见组;
//   - 判定规则与 HTTP 完全一致:canAccess(等级) / ItemVisibleRule(可见组),
//     确保 SMB 通道无法绕过条目级 ACL。
package smbgateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dgraph-io/ristretto"

	"gorm.io/gorm"

	"orbitcloud/core"
	"orbitcloud/model"
	"orbitcloud/server"
)

// sessionTTL 身份缓存有效期(60s:与 WatchAndPush 对账周期对齐,
// 用户权限/状态/组成员变更最迟 60s 生效)。
const sessionTTL = 60 * time.Second

// sessionRegistry 全局身份注册表(包级;BuildGateway 初始化)。
var sessionRegistry *SessionRegistry

// SessionInfo 会话用户信息(身份缓存值)。
type SessionInfo struct {
	UserID          uint
	Username        string
	PermissionLevel model.PermissionLevel
	Status          int
	// Groups 组成员 ID 列表(可见组判定用;随缓存 60s 过期刷新)。
	Groups []uint
}

// SessionRegistry 操作身份注册表:UserID → 用户信息。
// 实现:ristretto(大 key 规模优化的内存缓存,SetWithTTL 60s);
// Get 未命中 → 实时查库并回填(新注册用户立即可用)。
type SessionRegistry struct {
	cache *ristretto.Cache
}

// NewSessionRegistry 构造身份注册表。
// ristretto 参数:NumCounters=2^18 计数槽,MaxCost=2^20 条,BufferItems=64。
func NewSessionRegistry() (*SessionRegistry, error) {
	cache, err := ristretto.NewCache(&ristretto.Config{
		NumCounters: 1 << 18, // 计数槽(约 26 万条目规模)
		MaxCost:     1 << 20, // 最大条目数(预算 100 万)
		BufferItems: 64,      // 内部缓冲
	})
	if err != nil {
		return nil, fmt.Errorf("session registry: new cache: %w", err)
	}
	return &SessionRegistry{cache: cache}, nil
}

// Get 按 UserID 解析用户信息(缓存命中直接返回;miss 查库回填)。
// 用户不存在/已软删/已禁用 → 返回 (nil, server.ErrForbidden)(按不可用处理)。
func (r *SessionRegistry) Get(ctx context.Context, userID uint) (*SessionInfo, error) {
	if userID == 0 {
		return nil, server.ErrForbidden // 无身份标识 → 拒绝(防冒充)
	}
	if v, ok := r.cache.Get(uint64(userID)); ok {
		if info, ok := v.(*SessionInfo); ok {
			return info, nil // 缓存命中(60s 内数据)
		}
	}
	// 缓存 miss → 实时查库并回填
	info, err := loadSessionInfo(ctx, userID)
	if err != nil {
		return nil, err
	}
	r.cache.SetWithTTL(uint64(userID), info, 1, sessionTTL) // 回填(成本=1,TTL 60s)
	return info, nil
}

// Invalidate 主动失效某用户缓存(密码/权限变更即时推送时调用;
// 60s 全量刷新兜底)。
func (r *SessionRegistry) Invalidate(userID uint) {
	r.cache.Del(uint64(userID))
}

// loadSessionInfo 从 DB 加载用户信息(存在 + 状态正常 + 组成员)。
func loadSessionInfo(ctx context.Context, userID uint) (*SessionInfo, error) {
	var u model.User
	if err := core.DB.WithContext(ctx).First(&u, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, server.ErrForbidden // 用户不存在/已软删
		}
		return nil, fmt.Errorf("session registry: load user %d: %w", userID, err)
	}
	if u.Status != 1 {
		return nil, server.ErrForbidden // 账号禁用 → 按不可用处理
	}
	groups, err := server.UserGroupIDs(ctx, server.UserGroupIDsArg{UserID: userID})
	if err != nil {
		return nil, fmt.Errorf("session registry: load groups %d: %w", userID, err)
	}
	return &SessionInfo{
		UserID:          u.ID,
		Username:        u.Username,
		PermissionLevel: u.PermissionLevel,
		Status:          u.Status,
		Groups:          groups,
	}, nil
}

// ============================================================================
// 权限校验(与 HTTP api/perm.go 同口径)
// ============================================================================

// canAccessLevel 权限等级判定(数值越小权限越高;同 api.canAccess)。
func canAccessLevel(userPerm, needPerm model.PermissionLevel) bool {
	return userPerm <= needPerm
}

// checkBucketAccess 桶级校验:桶存在 + 状态正常 + 权限等级满足(同 HTTP
// precheckBucketWrite:桶可用 + permBucketAccess)。
func checkBucketAccess(ctx context.Context, info *SessionInfo, bucket *model.Bucket) error {
	if bucket.Status != 1 {
		return server.ErrForbidden // 桶禁用
	}
	// 属主直接放行;否则权限等级须满足桶访问要求
	if bucket.OwnerID == info.UserID {
		return nil
	}
	if !canAccessLevel(info.PermissionLevel, bucket.PermissionLevel) {
		return server.ErrForbidden
	}
	return nil
}

// checkItemVisible 条目级可见性(同 HTTP permItemAccessWith):
// 空可见组不限制;管理员可见一切;其余按组内成员判定。
func checkItemVisible(info *SessionInfo, visibleToGroups string) error {
	if strings.TrimSpace(visibleToGroups) == "" {
		return nil // 空 = 不限制
	}
	if info.PermissionLevel.IsAdmin() {
		return nil // 管理员可见一切(含受限条目)
	}
	groupIDs, err := server.ParseVisibleGroups(visibleToGroups)
	if err != nil {
		return server.ErrForbidden // 数据异常:拒绝访问(防越权)
	}
	if len(groupIDs) == 0 {
		return server.ErrForbidden
	}
	for _, gid := range info.Groups {
		for _, vgid := range groupIDs {
			if gid == vgid {
				return nil
			}
		}
	}
	return server.ErrForbidden
}

// checkAncestorsVisible 沿祖先链校验可用性与条目级可见性(同 HTTP
// permAncestorsAccessWith):任一祖先已删 → ErrNotFound(404 防探测);
// 每层同时做可见组判定。folderID=0(桶根)直接通过。
func checkAncestorsVisible(ctx context.Context, info *SessionInfo, bucketID, folderID uint) error {
	cur := folderID
	seen := map[uint]bool{} // 环保护(数据异常时防死循环)
	for cur != 0 && !seen[cur] {
		seen[cur] = true
		f, err := server.GetFolder(ctx, server.GetFolderArg{BucketID: bucketID, FolderID: cur})
		if err != nil {
			return err
		}
		if f.Isable == model.FolderUnusable {
			return server.ErrNotFound // 目录已删 → 不可达
		}
		if err := checkItemVisible(info, f.VisibleToGroups); err != nil {
			return err
		}
		cur = f.ParentID
	}
	return nil
}

// resolveSession 解析请求身份并校验用户可用(文件类操作统一入口)。
// 用户无效/禁用 → ErrForbidden(与 HTTP permUserActive 语义一致)。
func resolveSession(ctx context.Context, userID uint) (*SessionInfo, error) {
	info, err := sessionRegistry.Get(ctx, userID)
	if err != nil {
		return nil, err
	}
	smbDebugf("smb_gateway: 身份解析 userId=%d user=%q(权限=%d)", info.UserID, info.Username, info.PermissionLevel)
	return info, nil
}

// sessionKey context 键:已解析的操作者身份(dispatch 注入)。
type sessionKey struct{}

// sessionFrom 从上下文取操作者身份(dispatch 已注入;未注入 = 非文件操作)。
func sessionFrom(ctx context.Context) *SessionInfo {
	if v, ok := ctx.Value(sessionKey{}).(*SessionInfo); ok {
		return v
	}
	return nil
}

// withSession 把操作者身份写入上下文(dispatch 在文件类操作路由前调用)。
func withSession(ctx context.Context, info *SessionInfo) context.Context {
	return context.WithValue(ctx, sessionKey{}, info)
}
