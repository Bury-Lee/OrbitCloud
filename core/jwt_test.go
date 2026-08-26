package core

// jwt_test.go —— JwtService 单元测试:配置缺省归一、签发/校验往返、
// 防伪造(错密钥/篡改/过期/错 issuer/密钥混用)、HashToken 与 jti 唯一性。

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"orbitcloud/config"
	"orbitcloud/model"
)

// newTestJwt 构造测试用 JwtService(绕过 InitJwtServers 的 log.Fatalf 路径)。
func newTestJwt() *JwtService {
	return &JwtService{
		AccessSecret:  []byte("access-secret-for-test"),
		RefreshSecret: []byte("refresh-secret-for-test"),
		AccessTTL:     time.Minute,
		RefreshTTL:    time.Hour,
		Issuer:        "test-issuer",
		Algorithm:     "HS256",
	}
}

// ---------- InitJwtServers 配置归一 ----------

func TestInitJwtServersDefaults(t *testing.T) {
	j := InitJwtServers(&config.AccessJWT{Secret: "s3cr3t"}, nil)
	if string(j.RefreshSecret) == "s3cr3t" {
		t.Error("刷新密钥应派生而非复用访问密钥")
	}
	if len(j.RefreshSecret) != 64 {
		t.Errorf("派生刷新密钥应为 sha256 hex(64 字符), got %d", len(j.RefreshSecret))
	}
	if j.AccessTTL != 12*time.Hour {
		t.Errorf("AccessTTL = %v, want 12h", j.AccessTTL)
	}
	if j.RefreshTTL != 7*24*time.Hour {
		t.Errorf("RefreshTTL = %v, want 7d", j.RefreshTTL)
	}
	if j.Issuer != "orbitcloud" {
		t.Errorf("Issuer = %q, want orbitcloud", j.Issuer)
	}
	if j.Algorithm != "HS256" {
		t.Errorf("Algorithm = %q, want HS256", j.Algorithm)
	}
}

func TestInitJwtServersCustom(t *testing.T) {
	j := InitJwtServers(
		&config.AccessJWT{Secret: "a", ExpireMinutes: 30, Issuer: "me", Algorithm: "HS256"},
		&config.RefreshJWT{Secret: "r", ExpireMinutes: 60},
	)
	if string(j.RefreshSecret) != "r" {
		t.Error("显式刷新密钥应被采用")
	}
	if j.AccessTTL != 30*time.Minute || j.RefreshTTL != 60*time.Minute {
		t.Errorf("TTL = %v/%v, want 30m/60m", j.AccessTTL, j.RefreshTTL)
	}
	if j.Issuer != "me" {
		t.Errorf("Issuer = %q, want me", j.Issuer)
	}
}

func TestInitJwtServersAlgorithmFallback(t *testing.T) {
	// 不支持的算法回退 HS256(仅打 WARN 日志,不失败)
	j := InitJwtServers(&config.AccessJWT{Secret: "x", Algorithm: "RS256"}, nil)
	if j.Algorithm != "HS256" {
		t.Errorf("Algorithm = %q, want HS256 回退", j.Algorithm)
	}
}

// ---------- 访问令牌 ----------

func TestSignAccessVerifyRoundTrip(t *testing.T) {
	j := newTestJwt()
	tok, err := j.SignAccess(Claims{UserID: 42, Username: "alice", PermissionLevel: model.NormalUser})
	if err != nil {
		t.Fatalf("SignAccess err = %v", err)
	}
	claims, err := j.Verify(tok)
	if err != nil {
		t.Fatalf("Verify err = %v", err)
	}
	if claims.UserID != 42 || claims.Username != "alice" || claims.PermissionLevel != model.NormalUser {
		t.Errorf("claims = %+v, want uid=42/alice/NormalUser", claims)
	}
	if claims.Issuer != j.Issuer {
		t.Errorf("Issuer = %q, want %q", claims.Issuer, j.Issuer)
	}
	if claims.ID == "" {
		t.Error("应携带随机 jti")
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	j := newTestJwt()
	other := &JwtService{AccessSecret: []byte("different-secret")}
	tok, err := other.SignAccess(Claims{UserID: 1})
	if err != nil {
		t.Fatalf("SignAccess err = %v", err)
	}
	if _, err := j.Verify(tok); err == nil {
		t.Fatal("Verify 应拒绝错误密钥签发的令牌")
	}
}

func TestVerifyRejectsTamperedToken(t *testing.T) {
	j := newTestJwt()
	tok, err := j.SignAccess(Claims{UserID: 1, Username: "alice"})
	if err != nil {
		t.Fatalf("SignAccess err = %v", err)
	}
	// 篡改 payload 中的用户名(替换 alg 头与 payload 之间的数据)
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token 结构异常: %d 段", len(parts))
	}
	tampered := parts[0] + "." + base64Std(parts[1]) + "." + parts[2]
	if _, err := j.Verify(tampered); err == nil {
		t.Fatal("Verify 应拒绝被篡改的令牌")
	}
}

func TestVerifyRejectsRefreshSecretToken(t *testing.T) {
	// 用刷新密钥签发"访问格式"令牌 → Verify(访问) 必须拒绝(密钥混用防护)
	j := newTestJwt()
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{UserID: 1}).
		SignedString(j.RefreshSecret)
	if err != nil {
		t.Fatalf("sign err = %v", err)
	}
	if _, err := j.Verify(tok); err == nil {
		t.Fatal("Verify 应拒绝用刷新密钥签发的令牌")
	}
}

func TestVerifyRejectsExpiredToken(t *testing.T) {
	j := newTestJwt()
	// 手工构造已过期令牌(签发 API 会覆盖时间,故直接签名)
	expired := Claims{
		UserID: 1,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    j.Issuer,
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Minute)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Minute)),
		},
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, expired).SignedString(j.AccessSecret)
	if err != nil {
		t.Fatalf("sign err = %v", err)
	}
	if _, err := j.Verify(tok); err == nil {
		t.Fatal("Verify 应拒绝已过期令牌")
	}
}

func TestVerifyRejectsWrongIssuer(t *testing.T) {
	j := newTestJwt()
	foreign := Claims{
		UserID: 1,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "evil-issuer",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, foreign).SignedString(j.AccessSecret)
	if err != nil {
		t.Fatalf("sign err = %v", err)
	}
	if _, err := j.Verify(tok); err == nil {
		t.Fatal("Verify 应拒绝错误 issuer 的令牌")
	}
}

// ---------- 刷新令牌 ----------

func TestSignRefreshVerifyRefreshRoundTrip(t *testing.T) {
	j := newTestJwt()
	tok, err := j.SignRefresh(RefreshClaim{UserID: 7})
	if err != nil {
		t.Fatalf("SignRefresh err = %v", err)
	}
	claims, err := j.VerifyRefresh(tok)
	if err != nil {
		t.Fatalf("VerifyRefresh err = %v", err)
	}
	if claims.UserID != 7 {
		t.Errorf("UserID = %d, want 7", claims.UserID)
	}
}

func TestVerifyRefreshRejectsAccessSecretToken(t *testing.T) {
	j := newTestJwt()
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, RefreshClaim{UserID: 1}).
		SignedString(j.AccessSecret)
	if err != nil {
		t.Fatalf("sign err = %v", err)
	}
	if _, err := j.VerifyRefresh(tok); err == nil {
		t.Fatal("VerifyRefresh 应拒绝用访问密钥签发的令牌")
	}
}

// ---------- HashToken / jti ----------

func TestHashToken(t *testing.T) {
	a := HashToken("refresh-token-value")
	if len(a) != 64 {
		t.Errorf("HashToken 长度 = %d, want 64(sha256 hex)", len(a))
	}
	if a != HashToken("refresh-token-value") {
		t.Error("HashToken 应确定性")
	}
	if a == HashToken("refresh-token-value-2") {
		t.Error("不同令牌不应同哈希")
	}
}

func TestNewTokenIDUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := newTokenID()
		if len(id) != 32 {
			t.Fatalf("jti 长度 = %d, want 32(16 字节 hex)", len(id))
		}
		if seen[id] {
			t.Fatalf("jti 重复: %s", id)
		}
		seen[id] = true
	}
}

// base64Std 重编码 payload 段为不同内容(篡改辅助)。
func base64Std(s string) string {
	// 翻转大小写即改变 base64 文本,保证与原文不同
	b := []byte(s)
	out := make([]byte, len(b))
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z':
			out[i] = c - 32
		case c >= 'A' && c <= 'Z':
			out[i] = c + 32
		default:
			out[i] = c
		}
	}
	return string(out)
}
