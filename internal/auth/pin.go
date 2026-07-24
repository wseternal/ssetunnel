package auth

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
)

// GeneratePIN creates an 8-character uppercase base32 single-use enrollment PIN.
func GeneratePIN() (string, error) {
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate random PIN bytes: %w", err)
	}
	pin := strings.TrimRight(base32.StdEncoding.EncodeToString(b), "=")
	return strings.ToUpper(pin), nil
}
