// Package security implements API key generation/verification and upstream URL
// validation.
package security

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const prefixMarker = "gk_"

// KeyMaterial is the result of generating a new API key.
type KeyMaterial struct {
	Plaintext string
	Prefix    string
	Hash      []byte
}

// GenerateKey creates a key of the form gk_<env>_<prefix>_<secret>.
func GenerateKey(env, pepper string) (KeyMaterial, error) {
	env = sanitizeSegment(env)
	if env == "" {
		env = "dev"
	}
	prefixBytes := make([]byte, 6)
	if _, err := rand.Read(prefixBytes); err != nil {
		return KeyMaterial{}, err
	}
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return KeyMaterial{}, err
	}
	prefix := prefixMarker + env + "_" + hex.EncodeToString(prefixBytes)
	secret := hex.EncodeToString(secretBytes)
	return KeyMaterial{
		Plaintext: prefix + "_" + secret,
		Prefix:    prefix,
		Hash:      HashSecret(pepper, secret),
	}, nil
}

// ParseKey splits a presented key into its public prefix and secret.
func ParseKey(key string) (prefix, secret string, err error) {
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, prefixMarker) {
		return "", "", errors.New("invalid key prefix")
	}
	if strings.Count(key, "_") < 3 {
		return "", "", errors.New("invalid key format")
	}
	idx := strings.LastIndexByte(key, '_')
	if idx <= len(prefixMarker) || idx == len(key)-1 {
		return "", "", errors.New("invalid key format")
	}
	return key[:idx], key[idx+1:], nil
}

// HashSecret returns SHA-256(pepper || secret).
func HashSecret(pepper, secret string) []byte {
	h := sha256.New()
	h.Write([]byte(pepper))
	h.Write([]byte(secret))
	return h.Sum(nil)
}

// Verify compares a computed hash against the stored hash in constant time.
func Verify(stored, computed []byte) bool {
	return len(stored) == len(computed) && subtle.ConstantTimeCompare(stored, computed) == 1
}

// RandomToken returns a URL-safe random token.
func RandomToken(bytes int) (string, error) {
	if bytes <= 0 {
		bytes = 32
	}
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// SafePrefix returns a short, non-secret representation of a key prefix.
func SafePrefix(prefix string) string {
	if len(prefix) <= 20 {
		return prefix
	}
	return fmt.Sprintf("%s…%s", prefix[:12], prefix[len(prefix)-4:])
}

func sanitizeSegment(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
