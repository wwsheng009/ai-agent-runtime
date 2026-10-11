package push

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// VAPID holds the server's VAPID signing key (P-256).
type VAPID struct {
	private *ecdsa.PrivateKey
}

// LoadOrCreateVAPID loads the VAPID key from path (base64url of the private
// scalar) or creates and persists a new one. path "" is memory-only (tests).
func LoadOrCreateVAPID(path string) (*VAPID, error) {
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			if scalar, decErr := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(data))); decErr == nil && len(scalar) > 0 {
				curve := elliptic.P256()
				x, y := curve.ScalarBaseMult(scalar)
				return &VAPID{private: &ecdsa.PrivateKey{
					PublicKey: ecdsa.PublicKey{Curve: curve, X: x, Y: y},
					D:         new(big.Int).SetBytes(scalar),
				}}, nil
			}
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	if path != "" {
		if dir := filepath.Dir(path); dir != "" {
			_ = os.MkdirAll(dir, 0o700)
		}
		_ = os.WriteFile(path, []byte(base64.RawURLEncoding.EncodeToString(key.D.Bytes())), 0o600)
	}
	return &VAPID{private: key}, nil
}

// PublicKeyBase64 returns the uncompressed public key as base64url.
func (v *VAPID) PublicKeyBase64() string {
	if v == nil || v.private == nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), v.private.X, v.private.Y))
}

// SignVAPIDJWT builds a signed VAPID JWT (ES256) for the endpoint audience.
func (v *VAPID) SignVAPIDJWT(audience, subject string, ttl time.Duration) (string, error) {
	if v == nil || v.private == nil {
		return "", fmt.Errorf("push: no vapid key configured")
	}
	header, err := json.Marshal(map[string]string{"typ": "JWT", "alg": "ES256"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{
		"aud": audience,
		"exp": time.Now().Add(ttl).Unix(),
		"sub": subject,
	})
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, v.private, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
