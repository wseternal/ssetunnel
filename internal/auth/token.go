package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// ComputeDigest returns the SHA-256 hex string of the given input.
func ComputeDigest(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

// ComputeHMACDigest returns the HMAC-SHA256 hex string of the input using the given key.
// This is used for recovery code digests, which need a server-side secret (pepper)
// to resist offline brute-force attacks.
func ComputeHMACDigest(input, key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(input))
	return hex.EncodeToString(mac.Sum(nil))
}

// GenerateToken creates a cryptographically secure 64-hex character bearer token.
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate random token bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}
