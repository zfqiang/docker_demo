package auth

import (
	"testing"
	"time"
)

func TestParseRejectsExpiredAndTamperedTokens(t *testing.T) {
	// 同时覆盖有效、过期和错误密钥签名三种情况，确保验签与过期校验都生效。
	now := time.Unix(1_800_000_000, 0)
	token, err := Issue("test-secret", "user-1", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	claims, err := Parse("test-secret", token, now)
	if err != nil || claims.UserID != "user-1" {
		t.Fatalf("Parse() claims=%+v err=%v", claims, err)
	}
	if _, err := Parse("test-secret", token, now.Add(2*time.Minute)); err == nil {
		t.Fatal("expected expired token to be rejected")
	}
	if _, err := Parse("wrong-secret", token, now); err == nil {
		t.Fatal("expected invalid signature to be rejected")
	}
}
