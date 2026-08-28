package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const salt = "mitia-ops/v1"

type Cipher struct {
	aead cipher.AEAD
}

func New(masterKey string) (*Cipher, error) {
	if masterKey == "" {
		return nil, errors.New("master key cannot be empty")
	}
	key := sha256.Sum256([]byte(salt + ":" + masterKey))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

func (c *Cipher) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nil, nonce, []byte(plaintext), nil)
	return base64.RawStdEncoding.EncodeToString(append(nonce, sealed...)), nil
}

func (c *Cipher) Decrypt(ciphertextB64 string) (string, error) {
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(ciphertextB64))
	if err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("ciphertext too short")
	}
	nonce, body := raw[:ns], raw[ns:]
	plain, err := c.aead.Open(nil, nonce, body, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt (wrong key or corrupt data): %w", err)
	}
	return string(plain), nil
}
