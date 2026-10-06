package secrets

import (
	"errors"
	"strings"
	"testing"
)

const (
	keyA = "0000000000000000000000000000000000000000000000000000000000000001"
	keyB = "0000000000000000000000000000000000000000000000000000000000000002"
)

func TestSealAndOpenRoundTrip(t *testing.T) {
	ring, err := NewKeyring(keyA)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	sealed, err := ring.Seal("api_key", "sk-secret")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !IsSealed(sealed) || strings.Contains(sealed, "sk-secret") {
		t.Fatalf("sealed value leaks plaintext: %q", sealed)
	}
	opened, err := ring.Open("api_key", sealed)
	if err != nil || opened != "sk-secret" {
		t.Fatalf("Open = %q, %v", opened, err)
	}
	if _, err := ring.Open("other_purpose", sealed); !errors.Is(err, ErrCannotOpen) {
		t.Fatalf("Open with another purpose error = %v, want ErrCannotOpen", err)
	}
}

func TestPreviousKeysOnlyOpen(t *testing.T) {
	old, _ := NewKeyring(keyA)
	sealed, _ := old.Seal("api_key", "value")

	rotated, err := NewKeyring(keyB, keyA)
	if err != nil {
		t.Fatalf("NewKeyring rotated: %v", err)
	}
	if opened, err := rotated.Open("api_key", sealed); err != nil || opened != "value" {
		t.Fatalf("rotated Open = %q, %v", opened, err)
	}
	resealed, _ := rotated.Seal("api_key", "value")
	if _, err := old.Open("api_key", resealed); !errors.Is(err, ErrCannotOpen) {
		t.Fatalf("old keyring opened a value sealed by the rotated key: %v", err)
	}
}

func TestNilKeyringAndInvalidInput(t *testing.T) {
	var ring *Keyring
	if _, err := ring.Seal("p", "x"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("nil Seal error = %v", err)
	}
	if ring, err := NewKeyring(""); ring != nil || err != nil {
		t.Fatalf("empty key should yield nil keyring, got %v, %v", ring, err)
	}
	if _, err := NewKeyring("abc"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("short key error = %v", err)
	}
	ok, _ := NewKeyring(keyA)
	if _, err := ok.Open("p", "plain-text"); !errors.Is(err, ErrMalformed) {
		t.Fatalf("Open of unsealed value error = %v", err)
	}
	if _, err := ok.Open("p", "v1.!!!"); !errors.Is(err, ErrMalformed) {
		t.Fatalf("Open of bad base64 error = %v", err)
	}
}
