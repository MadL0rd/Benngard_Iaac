package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// signTestSHA256 — helper for tests only.
func signTestSHA256(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
