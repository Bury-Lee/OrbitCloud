// auth.go —— 动态认证:用户 / NT hash / ACL 查询与变更推送。
//
// 对应可行性报告 §3.2 级别 A(推荐):动态用户 / ACL 管理。
//   - Rust 侧 NTLM 校验仍在本机完成(内存用户表 + NT hash),性能无损;
//   - Go 侧是"权威源":用户建/改/删、桶增删、授权变更实时推送,无需重启。
//
// NT hash 约束(可行性报告 §4.1,必须处理):
//   - users 表存 bcrypt,无法反推 NT hash(NTLMv2 校验必须用 NT hash);
//   - 解法:users 表新增 nt_hash 列,密码设置/修改时由明文计算
//     NT hash = MD4(UTF-16LE(明文)),仅经本网关下发(Rust 侧持有)。
package smbgateway

import (
	"context" // 上下文(查询取消/超时传播)
	"errors"  // 错误判断(gorm.ErrRecordNotFound)
	"fmt"     // 格式化错误

	// 服务日志
	"strings" // 字符串处理(用户名去空白)
	"time"    // 时间(指纹比较)

	"gorm.io/gorm" // ORM:数据库访问

	"orbitcloud/model"  // 数据模型(User/Bucket/File/Folder)
	"orbitcloud/server" // server 层错误定义(ErrNotFound)
	"orbitcloud/utils"  // 工具(NT hash 计算)
)

// AuthService 动态认证服务:查询用户凭据、计算可见共享、推送变更。
// 所有方法为包级函数风格(遵循 server 层约定),直接访问 core 单例与 DB。
type AuthService struct {
	// db 数据库句柄(users / buckets / folders / files 表)。
	db *gorm.DB
	// pushCh 变更推送通道(DB 变更 → AclEntry;WatchAndPush 消费后广播)。
	pushCh chan AclEntry
	// pushFn 实际推送函数(由 BuildGateway 注入 Gateway.pushToAll)。
	pushFn func(entry AclEntry)
}

// NewAuthService 构造认证服务。
// 参数:db 数据库句柄(已初始化)。
// 返回值:认证服务实例。
func NewAuthService(db *gorm.DB) *AuthService {
	return &AuthService{
		db:     db,                       // 数据库句柄
		pushCh: make(chan AclEntry, 256), // 推送缓冲通道(容量 256,满则丢弃)
	}
}

// SetPushFunc 注入变更推送函数(网关启动后由装配层调用)。
func (a *AuthService) SetPushFunc(fn func(entry AclEntry)) {
	a.pushFn = fn // 保存推送函数(由 Gateway.pushToAll 实现)
}

// QueryUser 查询用户凭据(按用户名)。
// 参数:ctx 上下文;username 用户名(全局唯一)。
// 返回值:用户凭据(含 NT hash);未找到或已禁用返回 (nil, nil);错误为数据库错误。
func (a *AuthService) QueryUser(ctx context.Context, username string) (*UserCred, error) {
	var u model.User              // 查询目标行
	err := a.db.WithContext(ctx). // 带上下文查询
					Where("username = ?", strings.TrimSpace(username)). // 按去空白后的用户名精确匹配
					First(&u).Error                                     // 取首行
	if errors.Is(err, gorm.ErrRecordNotFound) { // 记录不存在?
		return nil, nil // 未找到:返回 nil(认证方按 LOGON_FAILURE 处理)
	}
	if err != nil { // 其它数据库错误
		return nil, fmt.Errorf("auth: query user %q: %w", username, err) // 带用户名报错
	}
	if u.Status != 1 { // 账号被禁用?
		return nil, nil // 禁用账号按不存在处理(认证方 LOGON_FAILURE)
	}
	cred := userCredFrom(&u)                                                      // 行 → UserCred
	smbDebugf("smb_gateway: 认证查询用户 %q 命中(权限=%d)", username, cred.PermissionLevel) // 记录查询命中(含权限级别)
	return cred, nil
}

// QuerySharesForUser 查询用户可见共享清单(动态 ACL 与共享拓扑)。
// 参数:ctx 上下文;username 用户名。
// 返回值:共享清单(ShareInfo 列表,已按可见性 ACL 过滤)。
// 规则:与 api 层桶访问口径一致 —— 桶 Status=1 且
// user.PermissionLevel <= bucket.PermissionLevel(数值越小权限越高),或桶属主。
func (a *AuthService) QuerySharesForUser(ctx context.Context, username string) ([]ShareInfo, error) {
	var u model.User                                                                                // 查询目标行
	err := a.db.WithContext(ctx).Where("username = ?", strings.TrimSpace(username)).First(&u).Error // 按用户名取用户
	if errors.Is(err, gorm.ErrRecordNotFound) {                                                     // 用户不存在?
		return []ShareInfo{}, nil // 返回空清单(非错误)
	}
	if err != nil { // 其它数据库错误
		return nil, fmt.Errorf("auth: query user %q: %w", username, err) // 带用户名报错
	}
	if u.Status != 1 { // 用户被禁用?
		return []ShareInfo{}, nil // 禁用用户无可见共享
	}
	shares, err := a.sharesForUser(ctx, &u) // 按用户行计算可见共享
	if err != nil {
		return nil, err // 计算失败直接上抛
	}
	if shares == nil { // 结果为 nil(无任何可见)?
		shares = []ShareInfo{} // 规范化为空切片(避免 JSON 序列化为 null)
	}
	return shares, nil // 返回可见共享清单
}

// sharesForUser 按用户行计算可见共享(内部;u 已加载)。
func (a *AuthService) sharesForUser(ctx context.Context, u *model.User) ([]ShareInfo, error) {
	// 注意:两次查询必须用独立 slice——GORM Find 是覆盖式填充,
	// 复用同一 slice 会让第二次查询覆盖第一次的结果。
	var owned []model.Bucket // 属主桶(owner_id 索引)
	if err := a.db.WithContext(ctx).
		Where("status = 1 AND owner_id = ?", u.ID).
		Order("id ASC").Find(&owned).Error; err != nil {
		return nil, fmt.Errorf("auth: list owned buckets for %q: %w", u.Username, err)
	}
	var accessible []model.Bucket // 非属主但权限级别达标的桶(permission_level 索引)
	if err := a.db.WithContext(ctx).
		Where("status = 1 AND permission_level >= ? AND owner_id != ?", u.PermissionLevel, u.ID).
		Order("id ASC").Find(&accessible).Error; err != nil {
		return nil, fmt.Errorf("auth: list accessible buckets for %q: %w", u.Username, err)
	}
	buckets := append(owned, accessible...)
	out := make([]ShareInfo, 0, len(buckets)) // 预分配结果切片
	for i := range buckets {                  // 遍历可见桶
		b := &buckets[i]             // 取桶行指针
		out = append(out, ShareInfo{ // 桶 → ShareInfo
			ShareName:  b.Name,      // 共享名 = 桶名
			BucketID:   b.ID,        // 桶主键
			BucketName: b.Name,      // 桶显示名
			Mode:       "readwrite", // 共享默认读写(当前固定)
			Users: []ShareUserAccess{{ // 该用户自己的 ACL 条目
				Username: u.Username,  // 用户名
				Access:   "readwrite", // 读写权限
			}},
			Quota:     b.Quota,     // 容量配额
			UsedSpace: b.UsedSpace, // 已用空间
			Status:    1,           // 桶正常
		})
	}
	return out, nil // 返回共享清单
}

// Snapshot 全量同步快照(用户 + 共享 + ACL)。
// 参数:ctx 上下文。
// 返回值:全量快照(启动/重连时 Rust 侧一次性拉取)。
// 组装:全部有效用户 → UserCred 列表;每个有效用户可见共享合并去重。
func (a *AuthService) Snapshot(ctx context.Context) (*SnapshotResult, error) {
	// 用户维度已改按需缓存(Rust 侧认证时经 CODE_AUTH_QUERY_USER 动态拉取,
	// 见 smb_server/rust/src/user_cache.rs),快照不再查询/下发用户;
	// 共享维度:全量桶清单(ACL 由认证时 CODE_AUTH_QUERY_ACL 按需授权,
	// 不再遍历全用户聚合)。
	var buckets []model.Bucket                                                                                 // 桶行集合
	if err := a.db.WithContext(ctx).Where("status = 1").Order("id ASC").Find(&buckets).Error; err != nil {     // 查全部启用桶
		return nil, fmt.Errorf("auth: snapshot buckets: %w", err) // 查询失败报错
	}
	shares := make([]ShareInfo, 0, len(buckets)) // 预分配共享列表
	for i := range buckets {                     // 遍历桶
		b := &buckets[i] // 桶行
		shares = append(shares, ShareInfo{       // 桶 → 共享(ACL 留空,按需授权)
			ShareName:  b.Name,      // 共享名
			BucketID:   b.ID,        // 桶 ID
			BucketName: b.Name,      // 桶名
			Mode:       "readwrite", // 读写模式
			Users:      []ShareUserAccess{}, // 非 nil 空数组(避免 JSON null 破坏 Rust 反序列化)
			Quota:      b.Quota,     // 配额
			UsedSpace:  b.UsedSpace, // 已用
			Status:     b.Status,    // 桶状态(1 正常/0 禁用)
		})
	}
	return &SnapshotResult{Users: []UserCred{}, Shares: shares}, nil // 快照 = 全部共享(用户按需)
}

// WatchAndPush 监听 DB 变更并推送(goroutine 常驻)。
// 策略:每 60s 重查快照与上次 diff,生成 upsert/delete 增量 AclEntry 推送;
// 初次运行时全量 upsert(幂等)。pushCh 满则丢弃(缓存 256 条,Rust 侧
// 断线重连后全量快照兜底)。
// 周期说明:客户端(内存用户表)允许滞后——服务端在每次认证/文件操作时
// 实时校验用户与桶状态(见 QueryUser/桶状态检查),异常用户即时拒绝;
// 本推送仅负责客户端状态收敛,60s 与 Rust 侧全量对账周期对齐。
func (a *AuthService) WatchAndPush(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second) // 60s 轮询定时器(与 Rust 侧对账周期对齐)
	defer ticker.Stop()                        // 退出时停止
	var last *SnapshotResult                   // 上次快照(初次 nil = 首次)

	push := func(entry AclEntry) { // 推送闭包:统一入口
		if a.pushFn == nil { // 推送函数未注入?
			return // 跳过(网关尚未装配完成)
		}
		a.pushFn(entry) // 实际推送(广播到全部连接)
	}

	// 变更推送消费者(SetUserNTHash 等主动推的条目)
	go func() { // 消费 pushCh 的独立协程
		for {
			select {
			case <-ctx.Done(): // 上下文取消?
				return // 退出消费者
			case entry := <-a.pushCh: // 收到主动推送条目
				push(entry) // 转发广播
			}
		}
	}()

	for { // 主循环:定时 diff + 推送
		select {
		case <-ctx.Done(): // 上下文取消?
			return // 退出轮询
		case <-ticker.C: // 60s 周期到
			snap, err := a.Snapshot(ctx) // 重拉全量快照
			if err != nil {
				smbErrorf("smb_gateway: 快照差异计算失败:%v", err) // 拉取失败:记日志跳过本轮
				continue
			}
			// 首次:全量 upsert(仅共享;用户维度按需缓存,不下发)
			if last == nil { // 首次运行?
				smbDebugf("smb_gateway: 监听首次运行,推送共享快照(%d 共享;用户按需缓存)", len(snap.Shares)) // 记录全量推送
				for i := range snap.Shares {                                                              // 全量推送共享
					push(AclEntry{Op: "upsert", Kind: "share", Share: &snap.Shares[i]}) // 逐条 upsert(幂等)
				}
				last = snap // 记录快照作为下次 diff 基准
				continue    // 跳到下一轮
			}
			// diff 共享(按 bucketID;用户维度按需缓存,不再推送用户变更)
			oldShares := map[uint]*ShareInfo{} // 旧共享表:桶 ID → 共享
			for i := range last.Shares {       // 遍历旧快照共享
				oldShares[last.Shares[i].BucketID] = &last.Shares[i] // 建索引
			}
			newShares := map[uint]*ShareInfo{} // 新共享表
			for i := range snap.Shares {       // 遍历新快照共享
				newShares[snap.Shares[i].BucketID] = &snap.Shares[i] // 建索引
			}
			shareUp, shareDel := 0, 0       // 共享变更计数(汇总日志)
			for id, ns := range newShares { // 检查新增/变更共享
				os, ok := oldShares[id]                                                                    // 旧快照有该桶?
				if !ok || os.Quota != ns.Quota || os.UsedSpace != ns.UsedSpace || os.Status != ns.Status { // 新增 或 配额/已用/状态变化?
					push(AclEntry{Op: "upsert", Kind: "share", Share: ns}) // 推送 upsert
					shareUp++                                              // 计数
				}
			}
			for id := range oldShares { // 检查删除共享
				if _, ok := newShares[id]; !ok { // 新快照已无该桶?
					push(AclEntry{Op: "delete", Kind: "share", Share: &ShareInfo{ShareName: oldShares[id].ShareName, BucketID: id}}) // 推送 delete(带共享名与桶 ID)
					shareDel++                                                                                                       // 计数
				}
			}
			if shareUp+shareDel > 0 { // 本轮有变更?
				smbDebugf("smb_gateway: 监听差异:共享 +%d -%d(用户按需缓存,不再全量推送)", shareUp, shareDel) // 记录本轮差异汇总
			}
			last = snap // 记录本轮快照为下轮基准
		}
	}
}

// SetUserNTHash 设置用户 NT hash(密码设置/修改流程钩子)。
// 参数:username 用户名;plainPassword 明文密码(仅此处使用,不留存)。
// 返回值:错误(用户不存在/计算失败)。
// 计算并更新 users.nt_hash 列,并向 pushCh 推送 upsert(新密码立即生效)。
func (a *AuthService) SetUserNTHash(ctx context.Context, username, plainPassword string) error {
	hash := ntHashOf(plainPassword) // 明文 → NT hash(MD4(UTF-16LE))
	res := a.db.WithContext(ctx).
		Model(&model.User{}).
		Where("username = ?", strings.TrimSpace(username)). // 定位用户
		Update("nt_hash", hash)                             // 更新 nt_hash 列
	if res.Error != nil { // 更新失败?
		return fmt.Errorf("auth: set nt_hash for %q: %w", username, res.Error) // 带用户名报错
	}
	if res.RowsAffected == 0 { // 无行受影响 = 用户不存在?
		return server.ErrNotFound // 返回未找到错误
	}
	cred, err := a.QueryUser(ctx, username) // 重查用户凭据(拿最新状态)
	if err == nil && cred != nil {          // 查询成功且用户有效?
		select {
		case a.pushCh <- AclEntry{Op: "upsert", Kind: "user", User: cred}: // 推送给消费者协程(立即生效)
		default: // 通道满?
			smbWarnf("smb_gateway: 推送通道已满,丢弃 %q 的 NT hash 更新", username) // 丢弃并记日志(全量快照兜底)
		}
	}
	return nil // 设置成功
}

// SyncUserNTHashFromPassword 计算并落库 NT hash(server 层注册/改密钩子调用)。
// 参数:username 用户名;plainPassword 明文密码。
// 返回值:错误。
func (a *AuthService) SyncUserNTHashFromPassword(ctx context.Context, username, plainPassword string) error {
	hash := ntHashOf(plainPassword) // 明文 → NT hash
	if err := a.db.WithContext(ctx).
		Model(&model.User{}).
		Where("username = ?", strings.TrimSpace(username)). // 定位用户
		Update("nt_hash", hash).Error; err != nil {         // 更新列(不推送,由 WatchAndPush 兜底)
		return fmt.Errorf("auth: sync nt_hash for %q: %w", username, err) // 失败带用户名报错
	}
	return nil // 落库成功
}

// ntHashOf 计算 NT hash(工具函数;委托 utils.NtHashOf,两侧共用)。
// 参数:plainPassword 明文密码。
// 返回值:NT hash 十六进制(32 字符)。
func ntHashOf(plainPassword string) string {
	return utils.NtHashOf(plainPassword) // 委托公共工具(与 server 层共用)
}

// userCredFrom 从 GORM 用户行组装 UserCred(内部工具)。
func userCredFrom(u *model.User) *UserCred {
	return &UserCred{ // 字段映射
		ID:              u.ID,                      // 用户主键(会话身份标识,Rust 侧操作帧携带)
		Username:        u.Username,                // 用户名
		NtHashHex:       strings.ToLower(u.NtHash), // NT hash 统一小写(与 Rust 侧 hex 约定一致)
		PermissionLevel: int8(u.PermissionLevel),   // 权限级别(0=超管)
		Status:          u.Status,                  // 状态(1 正常/0 禁用)
	}
}
