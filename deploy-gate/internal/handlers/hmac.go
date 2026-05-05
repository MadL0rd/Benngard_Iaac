package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// verifyHMAC — constant-time compare; bytes.Equal would leak the secret
// via timing.
func verifyHMAC(header, secret string, body []byte) bool {
	if !strings.HasPrefix(header, "sha256=") {
		return false
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(provided, mac.Sum(nil))
}
