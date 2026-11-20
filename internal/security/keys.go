// Package security implements API key generation/verification and upstream URL
// validation.
package security

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
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
	return parseKeyUnchecked(key)
}

func parseKeyUnchecked(key string) (prefix, secret string, err error) {
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

// DefaultMaxKeyBytes is a hard upper bound for presented API keys.
const DefaultMaxKeyBytes = 512

// ParseKeyLimited parses a key while rejecting pathological header sizes.
func ParseKeyLimited(key string, maxBytes int) (prefix, secret string, err error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxKeyBytes
	}
	if len(key) > maxBytes {
		return "", "", errors.New("key is too long")
	}
	return ParseKey(key)
}

// ValidateEnvName restricts provider secret references to an allowlisted env
// prefix, preventing an admin from exfiltrating unrelated process secrets.
func ValidateEnvName(name, prefix string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return errors.New("upstream key env prefix is empty")
	}
	if !strings.HasPrefix(name, prefix) {
		return fmt.Errorf("api_key_env must start with %q", prefix)
	}
	for _, r := range name {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return fmt.Errorf("api_key_env contains invalid character %q", r)
	}
	return nil
}
