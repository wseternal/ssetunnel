package sdk

import (
	"crypto/rand"
	"encoding/hex"
)

// randomHex returns a random hex string of the given byte length. Test files
// across this package share it to build unique Unix socket paths.
func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
