package encrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"
)

const ivLength = 12

type Service struct {
	key     []byte
	enabled bool
}

func New(encryptionKey string) *Service {
	if encryptionKey == "" {
		slog.Warn("No APP_ENCRYPTION_KEY configured. API keys will be stored as plain text.")
		return &Service{enabled: false}
	}
	hash := sha256.Sum256([]byte(encryptionKey))
	slog.Info("Encryption enabled with APP_ENCRYPTION_KEY")
	return &Service{key: hash[:], enabled: true}
}

func (s *Service) Enabled() bool { return s.enabled }

func (s *Service) Encrypt(plainText string) (string, error) {
	if plainText == "" || !s.enabled {
		return plainText, nil
	}

	block, err := aes.NewCipher(s.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	iv := make([]byte, ivLength)
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}

	encrypted := gcm.Seal(nil, iv, []byte(plainText), nil)

	combined := make([]byte, ivLength+len(encrypted))
	copy(combined[:ivLength], iv)
	copy(combined[ivLength:], encrypted)

	return base64.StdEncoding.EncodeToString(combined), nil
}

func (s *Service) Decrypt(cipherText string) (string, error) {
	if cipherText == "" {
		return cipherText, nil
	}

	if !s.enabled {
		if strings.HasPrefix(cipherText, "ENC:") {
			slog.Warn("Found encrypted data but no encryption key configured")
			return cipherText[4:], nil
		}
		return cipherText, nil
	}

	data := cipherText
	if strings.HasPrefix(data, "ENC:") {
		data = data[4:]
	}

	combined, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		// Not valid base64 — probably plain text
		return cipherText, nil
	}
	if len(combined) < ivLength {
		return cipherText, nil
	}

	iv := combined[:ivLength]
	encrypted := combined[ivLength:]

	block, err := aes.NewCipher(s.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	decrypted, err := gcm.Open(nil, iv, encrypted, nil)
	if err != nil {
		// Decryption failed — might be plain text or wrong key
		slog.Debug("Decryption failed, returning value as-is", "err", err)
		return cipherText, nil
	}

	return string(decrypted), nil
}

var ErrEncryptionDisabled = errors.New("encryption is not enabled")
