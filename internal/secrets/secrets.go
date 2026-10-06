// Package secrets seals small credentials, such as LLM provider API keys, so
// they can live in PostgreSQL without being readable by anyone who can read
// the table. The sealing key stays in the server environment.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	// tokenPrefix versions the on-disk format so a future cipher change can
	// coexist with values sealed today.
	tokenPrefix = "v1."
	keyBytes    = 32
)

var (
	ErrNoKey      = errors.New("secrets: STASH_SECRETS_KEY is not configured")
	ErrInvalidKey = errors.New("secrets: key must be 64 hexadecimal characters (32 bytes)")
	ErrMalformed  = errors.New("secrets: sealed value is malformed")
	ErrCannotOpen = errors.New("secrets: sealed value cannot be opened with the configured keys")
)

// Keyring holds the active sealing key plus older keys kept only for opening
// values sealed before a rotation. A nil Keyring is valid and refuses to seal.
type Keyring struct {
	active   cipher.AEAD
	previous []cipher.AEAD
}

// ParseKey decodes a 32-byte hexadecimal key such as `openssl rand -hex 32`.
func ParseKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	key, err := hex.DecodeString(raw)
	if err != nil || len(key) != keyBytes {
		return nil, ErrInvalidKey
	}
	return key, nil
}

// NewKeyring builds a keyring from hexadecimal keys. The first key seals new
// values; the rest only open existing ones. An empty active key yields nil so
// callers can treat "not configured" uniformly.
func NewKeyring(activeHex string, previousHex ...string) (*Keyring, error) {
	if strings.TrimSpace(activeHex) == "" {
		return nil, nil
	}
	active, err := newAEAD(activeHex)
	if err != nil {
		return nil, err
	}
	ring := &Keyring{active: active}
	for _, raw := range previousHex {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		aead, err := newAEAD(raw)
		if err != nil {
			return nil, fmt.Errorf("previous key: %w", err)
		}
		ring.previous = append(ring.previous, aead)
	}
	return ring, nil
}

func newAEAD(rawHex string) (cipher.AEAD, error) {
	key, err := ParseKey(rawHex)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: %w", err)
	}
	return cipher.NewGCM(block)
}

// Seal encrypts plaintext under the active key. purpose is bound as
// additional authenticated data so a value sealed for one column cannot be
// replayed into another.
func (k *Keyring) Seal(purpose, plaintext string) (string, error) {
	if k == nil || k.active == nil {
		return "", ErrNoKey
	}
	nonce := make([]byte, k.active.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secrets: nonce: %w", err)
	}
	sealed := k.active.Seal(nil, nonce, []byte(plaintext), []byte(purpose))
	return tokenPrefix + base64.RawURLEncoding.EncodeToString(append(nonce, sealed...)), nil
}

// Open decrypts a value produced by Seal, trying the active key first and
// then any previous keys.
func (k *Keyring) Open(purpose, sealed string) (string, error) {
	if k == nil || k.active == nil {
		return "", ErrNoKey
	}
	if !IsSealed(sealed) {
		return "", ErrMalformed
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(sealed, tokenPrefix))
	if err != nil {
		return "", ErrMalformed
	}
	for _, aead := range append([]cipher.AEAD{k.active}, k.previous...) {
		size := aead.NonceSize()
		if len(raw) < size {
			return "", ErrMalformed
		}
		plain, err := aead.Open(nil, raw[:size], raw[size:], []byte(purpose))
		if err == nil {
			return string(plain), nil
		}
	}
	return "", ErrCannotOpen
}

// IsSealed reports whether value carries the sealed-token prefix.
func IsSealed(value string) bool {
	return strings.HasPrefix(value, tokenPrefix)
}
