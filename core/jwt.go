// jwt.go —— JWT 服务:访问令牌(短时)与刷新令牌(长时)的签发/校验。
// 刷新令牌落库只存 SHA-256 哈希(见 HashToken),校验算法仅接受 HS256 并强制校验 issuer。
package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"orbitcloud/config"
	"orbitcloud/model"
)

// Claims 访问令牌负载。
type Claims struct {
	UserID          uint                   `json:"uid"`
	Username        string                 `json:"username"`
	PermissionLevel model.PermissionLevel  `json:"perm"`
	jwt.RegisteredClaims
}

// RefreshClaim 刷新令牌负载。
type RefreshClaim struct {
	UserID uint `json:"uid"`
	jwt.RegisteredClaims
}

// JwtService 访问令牌与刷新令牌管理。
type JwtService struct {
	AccessSecret  []byte        // 访问令牌签名密钥
	RefreshSecret []byte        // 刷新令牌签名密钥(为空时由访问密钥派生)
	AccessTTL     time.Duration // 访问令牌有效期(默认 720m)
	RefreshTTL    time.Duration // 刷新令牌有效期(默认 7d)
	Issuer        string        // 签发者
	Algorithm     string        // 签名算法(当前仅 HS256)
}

// InitJwtServers 由两段配置构造全局 JwtService,启动期调用一次并挂到 core.JWT。
// 访问密钥必填(缺失即终止启动);刷新密钥为空时由访问密钥 SHA-256 派生;TTL/Issuer/算法缺省归一。
func InitJwtServers(accessCfg *config.AccessJWT, refreshCfg *config.RefreshJWT) *JwtService {
	if accessCfg == nil {
		accessCfg = &config.AccessJWT{}
	}
	if refreshCfg == nil {
		refreshCfg = &config.RefreshJWT{}
	}
	accessSecret := strings.TrimSpace(accessCfg.Secret)
	if accessSecret == "" {
		log.Fatalf("jwt: access secret is required (生产禁止默认密钥,建议 orbitcloud_JWT_SECRET 注入)")
	}
	refreshSecret := strings.TrimSpace(refreshCfg.Secret)
	if refreshSecret == "" {
		sum := sha256.Sum256([]byte(accessSecret))
		refreshSecret = hex.EncodeToString(sum[:])
	}
	accessTTL := time.Duration(accessCfg.ExpireMinutes) * time.Minute
	if accessCfg.ExpireMinutes <= 0 {
		accessTTL = 12 * 60 * time.Minute
	}
	refreshTTL := time.Duration(refreshCfg.ExpireMinutes) * time.Minute
	if refreshCfg.ExpireMinutes <= 0 {
		refreshTTL = 7 * 24 * 60 * time.Minute
	}
	issuer := strings.TrimSpace(accessCfg.Issuer)
	if issuer == "" {
		issuer = "orbitcloud"
	}
	algorithm := strings.ToUpper(strings.TrimSpace(accessCfg.Algorithm))
	if algorithm == "" {
		algorithm = "HS256"
	}
	if algorithm != "HS256" {
		log.Printf("jwt: WARN unsupported algorithm %q, fallback to HS256", algorithm)
		algorithm = "HS256"
	}
	// jwt/v5 默认时间精度为秒,同一秒多次签发的令牌 iat/exp 相同(刷新令牌落库会唯一索引冲突);
	// 提到毫秒 + 签发时注入随机 jti 双重保障
	jwt.TimePrecision = time.Millisecond

	return &JwtService{
		AccessSecret:  []byte(accessSecret),
		RefreshSecret: []byte(refreshSecret),
		AccessTTL:     accessTTL,
		RefreshTTL:    refreshTTL,
		Issuer:        issuer,
		Algorithm:     algorithm,
	}
}

// signingMethod 当前仅支持 HS256。
func signingMethod() jwt.SigningMethod {
	return jwt.SigningMethodHS256
}

// newTokenID 生成随机 jti(16 字节 hex);rand 失败时退回纳秒时间戳。
func newTokenID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// SignAccess 签发访问令牌。入参 Claims 需已填充业务字段,函数补全 RegisteredClaims 后签名。
func (j *JwtService) SignAccess(c Claims) (token string, err error) {
	now := time.Now()
	c.ID = newTokenID()
	c.IssuedAt = &jwt.NumericDate{Time: now}
	c.ExpiresAt = &jwt.NumericDate{Time: now.Add(j.AccessTTL)}
	if j.Issuer != "" {
		c.Issuer = j.Issuer
	}
	t, err := jwt.NewWithClaims(signingMethod(), c).SignedString(j.AccessSecret)
	if err != nil {
		return "", fmt.Errorf("jwt: sign access token: %w", err)
	}
	return t, nil
}

// SignRefresh 签发刷新令牌。刷新令牌同时落库,服务端只存其哈希(HashToken)。
func (j *JwtService) SignRefresh(c RefreshClaim) (token string, err error) {
	now := time.Now()
	c.ID = newTokenID() // jti 随机,保证同一秒多次签发的令牌不同
	c.IssuedAt = &jwt.NumericDate{Time: now}
	c.ExpiresAt = &jwt.NumericDate{Time: now.Add(j.RefreshTTL)}
	if j.Issuer != "" {
		c.Issuer = j.Issuer
	}
	t, err := jwt.NewWithClaims(signingMethod(), c).SignedString(j.RefreshSecret)
	if err != nil {
		return "", fmt.Errorf("jwt: sign refresh token: %w", err)
	}
	return t, nil
}

// Verify 校验访问令牌:仅接受指定算法(防 alg=none/RS256 混淆)、强制校验 issuer 与 exp。
func (j *JwtService) Verify(token string) (*Claims, error) {
	claims := &Claims{}
	opts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{j.Algorithm}),
		jwt.WithExpirationRequired(),
	}
	if j.Issuer != "" {
		opts = append(opts, jwt.WithIssuer(j.Issuer))
	}
	_, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		return j.AccessSecret, nil // 固定返回密钥,不信任 token 头中的 kid/alg 换密钥
	}, opts...)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidToken, err)
	}
	return claims, nil
}

// VerifyRefresh 校验刷新令牌(供登录轮换使用),安全参数与 Verify 一致。
func (j *JwtService) VerifyRefresh(token string) (*RefreshClaim, error) {
	claims := &RefreshClaim{}
	opts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{j.Algorithm}),
		jwt.WithExpirationRequired(),
	}
	if j.Issuer != "" {
		opts = append(opts, jwt.WithIssuer(j.Issuer))
	}
	_, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		return j.RefreshSecret, nil
	}, opts...)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidToken, err)
	}
	return claims, nil
}

// HashToken 返回令牌的 SHA-256 哈希(刷新令牌存库比对用)。
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// errInvalidToken JWT 解析失败统一包装。
var errInvalidToken = errors.New("core: invalid token")
