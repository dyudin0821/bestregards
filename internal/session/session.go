// Package session issues and verifies stateless signed session tokens.
package session

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/dyudin0821/bestregards/internal/auth"
	"github.com/dyudin0821/bestregards/internal/user"
)

var _ auth.Tokens = (*Signer)(nil)

// ErrInvalid is returned for malformed, forged or expired tokens.
var ErrInvalid = errors.New("invalid session token")

type claims struct {
	UserID  int64 `json:"uid"`
	Expires int64 `json:"exp"`
}

// Signer creates and verifies tokens with a server-side secret.
type Signer struct {
	secret   []byte
	ttl      time.Duration
	guestTTL time.Duration
}

// New returns a Signer. The secret must be long enough to resist guessing.
func New(secret []byte, ttl time.Duration) (*Signer, error) {
	if len(secret) < 32 {
		return nil, errors.New("session secret must be at least 32 bytes")
	}
	return &Signer{secret: secret, ttl: ttl, guestTTL: ttl}, nil
}

// WithGuestTTL makes tokens of anonymous guests live that long instead. A guest has no other way
// back to the same account, so a short token would hand out a fresh daily allowance too often.
func (s *Signer) WithGuestTTL(ttl time.Duration) *Signer {
	s.guestTTL = ttl
	return s
}

// Issue returns a token for the user id, valid for the TTL of that kind of user.
func (s *Signer) Issue(userID int64, now time.Time) string {
	payload, _ := json.Marshal(claims{UserID: userID, Expires: now.Add(s.TTL(userID)).Unix()})
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + s.mac(body)
}

// Verify returns the user id from a valid, unexpired token.
func (s *Signer) Verify(token string, now time.Time) (int64, error) {
	body, sig, ok := strings.Cut(token, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(s.mac(body))) {
		return 0, ErrInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return 0, ErrInvalid
	}
	var c claims
	if err := json.Unmarshal(payload, &c); err != nil || c.UserID == 0 || now.Unix() >= c.Expires {
		return 0, ErrInvalid
	}
	return c.UserID, nil
}

// TTL reports how long tokens issued to userID live.
func (s *Signer) TTL(userID int64) time.Duration {
	if user.IsGuest(userID) {
		return s.guestTTL
	}
	return s.ttl
}

const macDomain = "bestregards.session.v1\x00"

func (s *Signer) mac(body string) string {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(macDomain)) // keeps a MAC from this signer from ever being valid for another purpose
	m.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
