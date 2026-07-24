package auth

import (
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

// GenerateToken creates a cryptographically secure 64-hex character bearer token.
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate random token bytes: %w", err)
	}
	return hex.EncodeToString(b), nil
}
