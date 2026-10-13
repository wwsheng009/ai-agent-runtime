package feishu

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// decryptEventPayload 解密飞书事件加密载荷（AES-256-CBC）：
//   - 密钥 = sha256(encrypt_key)（32 字节，飞书控制台配置的 Encrypt Key 原文哈希）；
//   - 载荷 = base64(iv[16] || ciphertext)，PKCS#7 填充；
//   - 非法 base64/长度/填充（含密钥错误导致的填充校验失败）一律返回 error，
//     由 HandleWebhook 抛给宿主转 400。
func decryptEventPayload(encrypted, encryptKey string) ([]byte, error) {
	key := sha256.Sum256([]byte(strings.TrimSpace(encryptKey)))

	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encrypted))
	if err != nil {
		return nil, fmt.Errorf("decode base64 payload: %s", err.Error())
	}
	if len(raw) <= aes.BlockSize || (len(raw)-aes.BlockSize)%aes.BlockSize != 0 {
		return nil, errors.New("ciphertext length is invalid for aes-256-cbc")
	}

	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("create aes cipher: %s", err.Error())
	}

	iv := raw[:aes.BlockSize]
	ciphertext := raw[aes.BlockSize:]
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plaintext, ciphertext)
	return pkcs7Unpad(plaintext, aes.BlockSize)
}

// pkcs7Unpad 校验并移除 PKCS#7 填充（AES 分组 16 字节）。
func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, errors.New("invalid pkcs7 data length")
	}
	padding := int(data[len(data)-1])
	if padding == 0 || padding > blockSize || padding > len(data) {
		return nil, errors.New("invalid pkcs7 padding")
	}
	for _, value := range data[len(data)-padding:] {
		if int(value) != padding {
			return nil, errors.New("invalid pkcs7 padding")
		}
	}
	return data[:len(data)-padding], nil
}
