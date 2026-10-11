// Package push implements Web Push for the gateway: VAPID signing, RFC 8291
// (aes128gcm) payload encryption and delivery to a browser push service, plus a
// small subscription store.
//
// Design: docs/gateway/05-wan-and-mobile.md §2.5, docs/gateway/08-notifications.md.
package push

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"

	"crypto/sha256"
	"golang.org/x/crypto/hkdf"
	"io"
)

// recordSize is the aes128gcm record size used for a single-record payload.
const recordSize = 4096

// lastRecordDelimiter marks the final (only) aes128gcm record.
const lastRecordDelimiter = 0x02

// EncryptPayload encrypts payload for the given subscription (RFC 8291). It
// returns the aes128gcm body and the ephemeral public key to send as the
// Content-Encoding keyid (already embedded in the body header).
func EncryptPayload(payload []byte, p256dh, auth string) ([]byte, error) {
	uaPublicBytes, err := base64.RawURLEncoding.DecodeString(p256dh)
	if err != nil {
		return nil, fmt.Errorf("push: decode p256dh: %w", err)
	}
	authSecret, err := base64.RawURLEncoding.DecodeString(auth)
	if err != nil {
		return nil, fmt.Errorf("push: decode auth: %w", err)
	}
	uaPublic, err := ecdh.P256().NewPublicKey(uaPublicBytes)
	if err != nil {
		return nil, fmt.Errorf("push: parse p256dh: %w", err)
	}
	asPrivate, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	asPublic := asPrivate.PublicKey().Bytes()

	ecdhSecret, err := asPrivate.ECDH(uaPublic)
	if err != nil {
		return nil, err
	}

	// IKM = HKDF(salt=auth_secret, ikm=ecdh_secret, info=auth_info, L=32)
	authInfo := append([]byte("WebPush: info\x00"), uaPublicBytes...)
	authInfo = append(authInfo, asPublic...)
	prkAuth := hkdf.Extract(sha256.New, ecdhSecret, authSecret)
	ikm := hkdfExpand(prkAuth, authInfo, 32)

	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	prk := hkdf.Extract(sha256.New, ikm, salt)
	cek := hkdfExpand(prk, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := hkdfExpand(prk, []byte("Content-Encoding: nonce\x00"), 12)

	// RFC 8188 header: salt(16) || rs(4) || idlen(1) || keyid.
	header := make([]byte, 0, 16+4+1+len(asPublic))
	header = append(header, salt...)
	rs := make([]byte, 4)
	binary.BigEndian.PutUint32(rs, recordSize)
	header = append(header, rs...)
	header = append(header, byte(len(asPublic)))
	header = append(header, asPublic...)

	plaintext := append(append([]byte(nil), payload...), lastRecordDelimiter)

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, header)

	body := make([]byte, 0, len(header)+len(ciphertext))
	body = append(body, header...)
	body = append(body, ciphertext...)
	return body, nil
}

func hkdfExpand(prk, info []byte, length int) []byte {
	out := make([]byte, length)
	reader := hkdf.Expand(sha256.New, prk, info)
	_, _ = io.ReadFull(reader, out)
	return out
}
