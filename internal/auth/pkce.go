package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// PKCEPair holds a code verifier and its corresponding challenge.
type PKCEPair struct {
	Verifier  string
	Challenge string
}

// GeneratePKCE creates a PKCE code verifier (64 random bytes, base64url-encoded)
// and its S256 challenge (SHA-256 of verifier, base64url-encoded, no padding).
func GeneratePKCE() (PKCEPair, error) {
	buf := make([]byte, 64)
	if _, err := rand.Read(buf); err != nil {
		return PKCEPair{}, err
	}

	verifier := base64.RawURLEncoding.EncodeToString(buf)

	hash := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(hash[:])

	return PKCEPair{
		Verifier:  verifier,
		Challenge: challenge,
	}, nil
}

// RandomState generates a random state parameter for CSRF protection.
func RandomState() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
