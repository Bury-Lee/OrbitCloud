// ratelimit.go —— 限流器(按 IP 与账号双维度令牌桶,防流量攻击)。
//
// 桶存储用已接入的 ristretto(TinyLFU 本地缓存,大 key 量友好):每个 IP/账号
// 一个令牌桶,SetWithTTL 空闲自动过期淘汰(无需手动清理协程);
// 令牌桶自实现(零新依赖)。被限流的请求记录(IP/账号/路径/时间)到内存环形
// 缓冲,供管理员查询。全局单例由 main 启动时 InitRateLimiter 初始化;
// 中间件见 api/middleware.go。
package server

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dgraph-io/ristretto"

	"orbitcloud/config"
)

// RateLimitRecord 被限流请求的记录。
type RateLimitRecord struct {
	Time     time.Time `json:"time"`     // 触发时间
	IP       string    `json:"ip"`       // 客户端 IP
	UserID   uint      `json:"user_id"`  // 账号(未登录 = 0)
	Username string    `json:"username"` // 账号名(未登录 = "")
	Method   string    `json:"method"`   // 请求方法
	Path     string    `json:"path"`     // 请求路径
	Reason   string    `json:"reason"`   // ip | user
}

// TokenBucket 简单令牌桶(每 1/rate 秒补 1 个令牌,容量 burst;并发安全)。
type TokenBucket struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
	rate   float64 // 每秒补充速率;<=0 = 不限
	burst  float64 // 容量
}

// newTokenBucket 新建令牌桶(初始装满,允许瞬时突发)。
func newTokenBucket(ratePerSec float64, burst int) *TokenBucket {
	if burst < 1 {
		burst = 1
	}
	return &TokenBucket{tokens: float64(burst), last: time.Now(), rate: ratePerSec, burst: float64(burst)}
}

// Allow 消费 1 个令牌;不足则拒绝。
func (b *TokenBucket) Allow() bool {
	if b.rate <= 0 {
		return true // 不限速
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens += now.Sub(b.last).Seconds() * b.rate
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// RateLimiter 全局限流器(ristretto 缓存桶,TTL 自动过期淘汰)。
type RateLimiter struct {
	cfg       *config.RateLimit
	ipCache   *ristretto.Cache // key = "ip:"+ip → *TokenBucket
	userCache *ristretto.Cache // key = "u:"+userID → *TokenBucket

	mu      sync.Mutex
	records []RateLimitRecord // 环形缓冲(被限流记录)
	pos     int
}

var (
	// RateLimit 全局限流器(未初始化 = 限流禁用)。
	RateLimit *RateLimiter
)

// InitRateLimiter 用配置初始化全局限流器(启动时调用一次;禁用时置 nil)。
// 桶由 ristretto SetWithTTL 管理:空闲超过 ttl 自动淘汰,无需手动清理。
func InitRateLimiter(cfg *config.RateLimit) error {
	if cfg == nil || !cfg.Enabled {
		RateLimit = nil
		return nil
	}
	ipCache, err := ristretto.NewCache(&ristretto.Config{
		NumCounters: 1 << 20, // 最多跟踪 ~100 万 key
		MaxCost:     1 << 20, // 桶数量上限(成本 1/桶)
		BufferItems: 64,
	})
	if err != nil {
		return err
	}
	userCache, err := ristretto.NewCache(&ristretto.Config{
		NumCounters: 1 << 20,
		MaxCost:     1 << 20,
		BufferItems: 64,
	})
	if err != nil {
		return err
	}
	RateLimit = &RateLimiter{
		cfg:       cfg,
		ipCache:   ipCache,
		userCache: userCache,
		records:   make([]RateLimitRecord, 0, recordLimit(cfg)),
	}
	return nil
}

// recordLimit 记录上限(默认 1000)。
func recordLimit(cfg *config.RateLimit) int {
	if cfg.RecordLimit <= 0 {
		return 1000
	}
	return cfg.RecordLimit
}

// ttl 桶空闲回收时间(默认 10 分钟;ristretto 到期自动淘汰)。
func (r *RateLimiter) ttl() time.Duration {
	if r.cfg.BucketTTLSeconds <= 0 {
		return 10 * time.Minute
	}
	return time.Duration(r.cfg.BucketTTLSeconds) * time.Second
}

// burstOf 突发容量(0 = 取速率的 2 倍,至少 1)。
func burstOf(ratePerSec float64, burst int) int {
	if burst <= 0 {
		b := int(ratePerSec * 2)
		if b < 1 {
			b = 1
		}
		return b
	}
	return burst
}

// Enabled 是否启用(未初始化或配置禁用 → false)。
func (r *RateLimiter) Enabled() bool { return r != nil && r.cfg.Enabled }

// SkipPaths 放行路径前缀列表。
func (r *RateLimiter) SkipPaths() []string { return r.cfg.SkipPaths }

// ipBucket 取(或建)IP 令牌桶。
func (r *RateLimiter) ipBucket(ip string) *TokenBucket {
	key := "ip:" + ip
	if v, ok := r.ipCache.Get(key); ok {
		return v.(*TokenBucket)
	}
	b := newTokenBucket(r.cfg.IPRate, burstOf(r.cfg.IPRate, r.cfg.IPBurst))
	r.ipCache.SetWithTTL(key, b, 1, r.ttl())
	return b
}

// userBucket 取(或建)账号令牌桶。
func (r *RateLimiter) userBucket(userID uint) *TokenBucket {
	key := fmt.Sprintf("u:%d", userID)
	if v, ok := r.userCache.Get(key); ok {
		return v.(*TokenBucket)
	}
	b := newTokenBucket(r.cfg.UserRate, burstOf(r.cfg.UserRate, r.cfg.UserBurst))
	r.userCache.SetWithTTL(key, b, 1, r.ttl())
	return b
}

// AllowIP 是否放行该 IP(ip_rate<=0 恒放行)。
func (r *RateLimiter) AllowIP(ip string) bool {
	if r.cfg.IPRate <= 0 {
		return true
	}
	return r.ipBucket(ip).Allow()
}

// AllowUser 是否放行该账号(user_rate<=0 恒放行)。
func (r *RateLimiter) AllowUser(userID uint) bool {
	if r.cfg.UserRate <= 0 {
		return true
	}
	return r.userBucket(userID).Allow()
}

// Record 记录一次被限流的请求(环形缓冲,超上限覆盖最旧)。
func (r *RateLimiter) Record(rec RateLimitRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cap := recordLimit(r.cfg)
	if len(r.records) < cap {
		r.records = append(r.records, rec)
		return
	}
	r.records[r.pos%cap] = rec
	r.pos++
}

// Records 被限流请求记录快照(最新在前)。
func (r *RateLimiter) Records() []RateLimitRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]RateLimitRecord, len(r.records))
	copy(out, r.records)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Stats 限流器状态(供管理员查询)。
func (r *RateLimiter) Stats() map[string]any {
	if r == nil {
		return map[string]any{"enabled": false}
	}
	return map[string]any{
		"enabled":      r.cfg.Enabled,
		"ip_rate":      r.cfg.IPRate,
		"ip_burst":     burstOf(r.cfg.IPRate, r.cfg.IPBurst),
		"user_rate":    r.cfg.UserRate,
		"user_burst":   burstOf(r.cfg.UserRate, r.cfg.UserBurst),
		"skip_paths":   strings.Join(r.cfg.SkipPaths, ","),
		"record_count": len(r.Records()),
	}
}
