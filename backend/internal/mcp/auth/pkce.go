package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// PKCE 是一次授权流程使用的 Proof Key for Code Exchange（RFC 7636）。
type PKCE struct {
	Verifier  string
	Challenge string
	Method    string
}

// NewPKCE 生成 S256 方式的 PKCE（verifier 为 32 字节随机数的 base64url 无填充编码）。
func NewPKCE() (PKCE, error) {
	verifier, err := randomURLSafe(32)
	if err != nil {
		return PKCE{}, err
	}
	return PKCE{
		Verifier:  verifier,
		Challenge: ChallengeS256(verifier),
		Method:    "S256",
	}, nil
}

// ChallengeS256 计算 PKCE S256 challenge。
func ChallengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// NewState 生成授权请求的 state（CSRF 防护）。
func NewState() (string, error) {
	return randomURLSafe(24)
}

func randomURLSafe(n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("随机字节数必须为正")
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成随机数失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
