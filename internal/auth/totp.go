package auth

import (
	"fmt"
	"time"

	"github.com/pquerna/otp/totp"
)

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
