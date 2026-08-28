// ratelimit_test.go —— 限流器测试:令牌桶补充/突发/限速、双维度(IP/账号)、
// 被限流记录环形缓冲、禁用配置不初始化。
package server

import (
	"testing"
	"time"

	"orbitcloud/config"
)

func TestTokenBucket(t *testing.T) {
	// 速率 10/s,突发 3:立刻可消费 3 个,第 4 个被拒
	b := newTokenBucket(10, 3)
	allowed := 0
	for i := 0; i < 4; i++ {
		if b.Allow() {
			allowed++
		}
	}
	if allowed != 3 {
		t.Fatalf("burst allow = %d, want 3", allowed)
	}

	// 等待 ~110ms(补充约 1 个令牌)后可再消费 1 个
	time.Sleep(110 * time.Millisecond)
	if !b.Allow() {
		t.Fatal("should allow after refill")
	}
	if b.Allow() {
		t.Fatal("should reject after consuming refill")
	}

	// 不限速(rate<=0)恒放行
	u := newTokenBucket(0, 1)
	for i := 0; i < 5; i++ {
		if !u.Allow() {
			t.Fatal("rate<=0 should always allow")
		}
	}
}

func TestRateLimiterIPAndUser(t *testing.T) {
	// 启用:IP 速率 5/s 突发 2;账号速率 3/s 突发 1
	cfg := &config.RateLimit{
		Enabled:          true,
		IPRate:           5,
		IPBurst:          2,
		UserRate:         3,
		UserBurst:        1,
		RecordLimit:      10,
		BucketTTLSeconds: 60,
	}
	if err := InitRateLimiter(cfg); err != nil {
		t.Fatalf("init: %v", err)
	}
	defer func() { RateLimit = nil }()
	rl := RateLimit

	// IP 维度:预热创建桶(等 ristretto Set 生效;预热消耗 1 个,桶剩 1),
	// 再连续 3 次:仅第 1 次通过(突发 2 - 预热 1),后 2 次被拒
	rl.AllowIP("1.2.3.4")
	time.Sleep(20 * time.Millisecond) // 等 Set flush,后续 Get 命中同一桶
	ok := 0
	for i := 0; i < 3; i++ {
		if rl.AllowIP("1.2.3.4") {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("ip allow = %d, want 1(突发 2 - 预热 1)", ok)
	}
	// 不同 IP 互不影响
	if !rl.AllowIP("5.6.7.8") {
		t.Fatal("different ip should be independent")
	}

	// 账号维度:预热消耗 1 个(突发=1),等 ~400ms 补充 1 个令牌后再通过 1 次
	rl.AllowUser(42)
	time.Sleep(20 * time.Millisecond)
	if rl.AllowUser(42) {
		t.Fatal("user second should be rejected(预热已耗尽突发)")
	}
	time.Sleep(400 * time.Millisecond) // 3/s → 补充 1.2 个
	if !rl.AllowUser(42) {
		t.Fatal("user should allow after refill")
	}
	if rl.AllowUser(42) {
		t.Fatal("user should reject after consuming refill")
	}
	// 不同账号互不影响
	if !rl.AllowUser(43) {
		t.Fatal("different user should be independent")
	}

	// 记录:2 条,最新在前
	rl.Record(RateLimitRecord{IP: "1.1.1.1", Path: "/a", Reason: "ip"})
	rl.Record(RateLimitRecord{IP: "2.2.2.2", Path: "/b", Reason: "user"})
	recs := rl.Records()
	if len(recs) != 2 || recs[0].IP != "2.2.2.2" {
		t.Fatalf("records = %+v, want 2 条且最新在前", recs)
	}
	if !rl.Enabled() {
		t.Fatal("should be enabled")
	}
}

func TestRateLimiterDisabled(t *testing.T) {
	// 配置禁用 → 不初始化(单例 nil)
	if err := InitRateLimiter(&config.RateLimit{Enabled: false}); err != nil {
		t.Fatalf("init disabled: %v", err)
	}
	if RateLimit != nil {
		t.Fatal("disabled config should leave RateLimit nil")
	}
}
