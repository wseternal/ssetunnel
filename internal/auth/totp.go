package auth

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"time"

	"github.com/pquerna/otp/totp"
)

// recoveryCodeChars is the character set for generating recovery codes.
// Alphanumeric without ambiguous characters (0/O, 1/l/I).
const recoveryCodeChars = "abcdefghjkmnpqrstuvwxyz23456789"

// VerifyTOTP validates a 6-digit TOTP code against a base32 secret.
func VerifyTOTP(secret, code string) bool {
	valid, err := totp.ValidateCustom(code, secret, time.Now().UTC(), totp.ValidateOpts{
		Period:    30,
		Skew:      1,
		Digits:    6,
		Algorithm: 0, // HMACSHA1
	})
	return err == nil && valid
}

// GenerateTOTPCode generates a valid TOTP passcode for testing/verification.
func GenerateTOTPCode(secret string) (string, error) {
	code, err := totp.GenerateCode(secret, time.Now().UTC())
	if err != nil {
		return "", fmt.Errorf("failed to generate TOTP code: %w", err)
	}
	return code, nil
}

// GenerateTOTPSecret creates a new random TOTP key for a given issuer and account name.
func GenerateTOTPSecret(issuer, accountName string) (secret string, keyURL string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: accountName,
	})
	if err != nil {
		return "", "", fmt.Errorf("failed to generate TOTP key: %w", err)
	}
	return key.Secret(), key.URL(), nil
}

// GenerateRecoveryCodes generates n random 10-character alphanumeric recovery codes.
// Each code uses an unambiguous character set (no 0/O, 1/l/I).
func GenerateRecoveryCodes(n int) ([]string, error) {
	if n <= 0 {
		return nil, fmt.Errorf("count must be positive, got %d", n)
	}
	charset := []byte(recoveryCodeChars)
	codes := make([]string, n)
	for i := range codes {
		code := make([]byte, 10)
		for j := range code {
			idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
			if err != nil {
				return nil, fmt.Errorf("failed to generate random byte: %w", err)
			}
			code[j] = charset[idx.Int64()]
		}
		codes[i] = string(code)
	}
	return codes, nil
}
