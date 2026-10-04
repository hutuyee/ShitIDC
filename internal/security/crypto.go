package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
)

func Encrypt(master []byte, plaintext string) (string, error) {
	if len(master) != 32 {
		return "", fmt.Errorf("master key must be 32 bytes")
	}
	block, err := aes.NewCipher(master)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	buf := append(nonce, sealed...)
	return base64.RawStdEncoding.EncodeToString(buf), nil
}

func Decrypt(master []byte, encoded string) (string, error) {
	if len(master) != 32 {
		return "", fmt.Errorf("master key must be 32 bytes")
	}
	buf, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(master)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(buf) < gcm.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}
	plain, err := gcm.Open(nil, buf[:gcm.NonceSize()], buf[gcm.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
