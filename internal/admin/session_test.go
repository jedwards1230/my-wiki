package admin

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

const (
	testKeyA = "this-is-a-sufficiently-long-key-aaaa"
	testKeyB = "this-is-a-sufficiently-long-key-bbbb"
)

func TestSecretCodecRoundTrip(t *testing.T) {
	c, err := newSecretCodec([]string{testKeyA})
	if err != nil {
		t.Fatalf("newSecretCodec: %v", err)
	}
	in := session{Subject: "u1", Groups: []string{"admins"}, CSRF: "tok", Expiry: 123}
	tokenStr, err := c.seal(in)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	var out session
	if err := c.open(tokenStr, &out); err != nil {
		t.Fatalf("open: %v", err)
	}
	if out.Subject != in.Subject || out.CSRF != in.CSRF || len(out.Groups) != 1 {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}

func TestSecretCodecRejectsShortKey(t *testing.T) {
	if _, err := newSecretCodec([]string{"too-short"}); err == nil {
		t.Fatal("expected error for short key")
	}
	if _, err := newSecretCodec(nil); err == nil {
		t.Fatal("expected error for empty key list")
	}
}

func TestSecretCodecRejectsTamper(t *testing.T) {
	c, _ := newSecretCodec([]string{testKeyA})
	tokenStr, _ := c.seal(session{Subject: "u1"})

	// Decode, flip a byte in the ciphertext, re-encode — guarantees a real
	// change so we exercise GCM authentication rather than base64 decoding.
	raw, err := base64.RawURLEncoding.DecodeString(tokenStr)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	raw[len(raw)-1] ^= 0xFF
	tampered := base64.RawURLEncoding.EncodeToString(raw)

	var out session
	if err := c.open(tampered, &out); err == nil {
		t.Fatal("expected authentication failure on tampered token")
	}
}

func TestSecretCodecKeyRotation(t *testing.T) {
	// Cookie sealed with the old key (B as primary).
	old, _ := newSecretCodec([]string{testKeyB})
	tokenStr, _ := old.seal(session{Subject: "rotated"})

	// New codec lists the new key first, old key as fallback.
	rotated, _ := newSecretCodec([]string{testKeyA, testKeyB})
	var out session
	if err := rotated.open(tokenStr, &out); err != nil {
		t.Fatalf("expected fallback key to open old cookie: %v", err)
	}
	if out.Subject != "rotated" {
		t.Fatalf("unexpected subject %q", out.Subject)
	}

	// A codec without the old key must reject it.
	onlyNew, _ := newSecretCodec([]string{testKeyA})
	if err := onlyNew.open(tokenStr, &out); err == nil {
		t.Fatal("expected rejection when signing key is absent")
	}
}

func TestSessionExpired(t *testing.T) {
	if (&session{Expiry: time.Now().Add(time.Hour).Unix()}).expired() {
		t.Fatal("future expiry should not be expired")
	}
	if !(&session{Expiry: time.Now().Add(-time.Hour).Unix()}).expired() {
		t.Fatal("past expiry should be expired")
	}
}

func TestSanitizeReturn(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", adminRoot},
		{"/_/admin/lint-config", "/_/admin/lint-config"},
		{"/_/admin/", "/_/admin/"},
		{"https://evil.example.com", adminRoot},
		{"//evil.example.com/path", adminRoot},
		{"/etc/passwd", adminRoot},
		{"/_/admin/../../secret", adminRoot},
		{"/other/path", adminRoot},
	}
	for _, tc := range cases {
		if got := sanitizeReturn(tc.in); got != tc.want {
			t.Errorf("sanitizeReturn(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestConstantTimeEqual(t *testing.T) {
	if !constantTimeEqual("abc", "abc") {
		t.Fatal("equal strings should match")
	}
	if constantTimeEqual("abc", "abd") || constantTimeEqual("abc", "abcd") {
		t.Fatal("unequal strings should not match")
	}
}

func TestRandomTokenDistinct(t *testing.T) {
	a, err := randomToken(24)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := randomToken(24)
	if a == b || a == "" {
		t.Fatalf("expected distinct non-empty tokens, got %q %q", a, b)
	}
	if strings.ContainsAny(a, "+/=") {
		t.Fatalf("expected base64url (no +/=), got %q", a)
	}
}
