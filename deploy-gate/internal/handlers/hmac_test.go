package handlers

import (
	"strings"
	"testing"
)

func TestVerifyHMAC(t *testing.T) {
	body := []byte(`{"image":"exo.container-registry.com/x/y:v1.2.3"}`)
	secret := "topsecret"

	// Reject obviously-wrong inputs.
	cases := []struct {
		name   string
		header string
		body   []byte
		want   bool
	}{
		{"empty header", "", body, false},
		{"no prefix", "df40c34f", body, false},
		{"bad hex", "sha256=zzz", body, false},
		{"wrong digest", "sha256=" + strings.Repeat("0", 64), body, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := verifyHMAC(testCase.header, secret, testCase.body)
			if result != testCase.want {
				t.Errorf("verifyHMAC(%q, _, _) = %v, want %v",
					testCase.header, result, testCase.want)
			}
		})
	}

	// Round-trip: sign with the right secret and check the verifier passes,
	// then check that the same signature fails against a different secret.
	signature := signTestSHA256(secret, body)
	if !verifyHMAC("sha256="+signature, secret, body) {
		t.Fatal("round-trip verify failed")
	}
	if verifyHMAC("sha256="+signature, "wrong-secret", body) {
		t.Fatal("verify accepted wrong secret")
	}

	// Tamper detection: any change to the body should make the signature
	// stop verifying.
	tamperedBody := append([]byte{}, body[:len(body)-1]...)
	if verifyHMAC("sha256="+signature, secret, tamperedBody) {
		t.Fatal("verify accepted tampered body")
	}
}
