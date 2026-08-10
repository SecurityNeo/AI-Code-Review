package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
)

// encryptionKey 从环境变量读取（优先 ENCRYPTION_KEY，其次 CODEGUARD_ENCRYPTION_KEY）
// 如果没有配置，使用明文模式（返回原始字符串，不加密）
var encryptionKey []byte
var encryptionEnabled bool

func init() {
	key := os.Getenv("ENCRYPTION_KEY")
	if key == "" {
		key = os.Getenv("CODEGUARD_ENCRYPTION_KEY")
	}
	if key != "" {
		encryptionKey = []byte(key)
		encryptionEnabled = true
	}
}

// EncryptString AES-GCM 加密，如果未配置密钥则返回明文
func EncryptString(plaintext string) (string, error) {
	if !encryptionEnabled {
		// 未配置加密密钥，使用明文保存
		return plaintext, nil
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return "", fmt.Errorf("创建 cipher 失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("创建 GCM 失败: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("生成 nonce 失败: %w", err)
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// DecryptString AES-GCM 解密，如果未配置密钥则返回原始字符串（兼容明文存储）
func DecryptString(ciphertext string) (string, error) {
	if !encryptionEnabled {
		// 未配置加密密钥，直接返回原始字符串（兼容明文存储的历史数据）
		return ciphertext, nil
	}
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		// 如果解码失败，可能是明文存储的数据，直接返回
		return ciphertext, nil
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return "", fmt.Errorf("创建 cipher 失败: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("创建 GCM 失败: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		// 数据太短，可能是明文
		return ciphertext, nil
	}
	nonce, ciphertextBytes := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertextBytes, nil)
	if err != nil {
		// 解密失败，可能是明文存储的数据，直接返回原始字符串
		return ciphertext, nil
	}
	return string(plaintext), nil
}

// MaskString 生成脱敏字符串（前4 + **** + 后4）
func MaskString(s string) string {
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "****" + s[len(s)-4:]
}

// EnvEncryptionKeyConfigured 返回加密密钥是否已配置环境变量
func EnvEncryptionKeyConfigured() bool {
	return encryptionEnabled
}
