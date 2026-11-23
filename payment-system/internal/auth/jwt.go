package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrUnauthorized 表示 token 缺失、签名错误、格式错误或已经过期。
var ErrUnauthorized = errors.New("unauthorized")

type contextKey struct{}

// Claims 是本项目使用的最小 JWT 负载：sub 为业务用户标识，exp 为 Unix 秒过期时间。
type Claims struct {
	UserID string `json:"sub"`
	Expiry int64  `json:"exp"`
}

// Issue 使用 HS256 签发无第三方依赖的开发/演示 token。
func Issue(secret, userID string, expiresAt time.Time) (string, error) {
	if userID == "" || secret == "" {
		return "", errors.New("user ID and JWT secret are required")
	}
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	payload, err := json.Marshal(Claims{UserID: userID, Expiry: expiresAt.Unix()})
	if err != nil {
		return "", fmt.Errorf("encode JWT claims: %w", err)
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload)
	return unsigned + "." + sign(secret, unsigned), nil
}

// Parse 校验签名、Base64 负载、用户标识和过期时间；所有校验失败统一返回 ErrUnauthorized。
func Parse(secret, token string, now time.Time) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || secret == "" {
		return Claims{}, ErrUnauthorized
	}
	unsigned := parts[0] + "." + parts[1]
	if !hmac.Equal([]byte(sign(secret, unsigned)), []byte(parts[2])) {
		return Claims{}, ErrUnauthorized
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, ErrUnauthorized
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil || claims.UserID == "" || claims.Expiry <= now.Unix() {
		return Claims{}, ErrUnauthorized
	}
	return claims, nil
}

// UserID 从上下文读取鉴权中间件注入的已验签用户标识。
func UserID(ctx context.Context) string {
	userID, _ := ctx.Value(contextKey{}).(string)
	return userID
}

func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, contextKey{}, userID)
}

func sign(secret, message string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(message))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func ParseExpiry(value string) (time.Time, error) {
	unix, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid expiry: %w", err)
	}
	return time.Unix(unix, 0), nil
}
