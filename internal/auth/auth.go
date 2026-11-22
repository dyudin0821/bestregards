// Package auth holds the sign-in use cases. It defines the ports it needs (storage, init data
// verification, token signing) and knows nothing about HTTP, SQL or Telegram's wire format.
package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dyudin0821/bestregards/internal/user"
)

// Errors the delivery layer maps to responses.
var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrSessionInvalid     = errors.New("invalid session")
	ErrBanned             = errors.New("user is banned")
	ErrChallengeFailed    = errors.New("challenge failed")
	// ErrGuestsDisabled means an operator stopped handing out guest accounts. Guest returns it
	// before it looks at the challenge; sessions of guests who already exist keep working.
	ErrGuestsDisabled = errors.New("guest accounts are switched off")
)

// Identity is who Telegram vouches for after a successful verification.
type Identity struct {
	UserID     int64
	FirstName  string // shown in the greeting, never stored
	StartParam string
}

// Ports, implemented by outer layers.

// GuestSwitch lets an operator stop new guest accounts. It is part of Users, so a storage that
// cannot answer cannot be wired in by mistake and the switch is always consulted.
type GuestSwitch interface {
	GuestsOff(ctx context.Context) (bool, error)
}

// Users persists users.
type Users interface {
	GuestSwitch
	UpsertUser(ctx context.Context, id int64, source string) (user.User, error)
	GetUser(ctx context.Context, id int64) (user.User, error) // returns user.ErrNotFound
	CreateGuest(ctx context.Context) (user.User, error)
}

// Challenger tells people from scripts before a guest account is handed out.
type Challenger interface {
	// Check returns an error wrapping ErrChallengeFailed when the answer is wrong, and any other
	// error when it could not decide.
	Check(ctx context.Context, response, ip string) error
}

// InitDataVerifier checks Telegram Mini App launch data.
type InitDataVerifier interface {
	Verify(raw string, now time.Time) (Identity, error)
}

// Tokens issues and verifies session tokens.
type Tokens interface {
	Issue(userID int64, now time.Time) string
	Verify(token string, now time.Time) (int64, error)
	TTL(userID int64) time.Duration
}

// Session is the result of a successful sign-in.
type Session struct {
	Token     string
	ExpiresIn time.Duration
	UserID    int64
	FirstName string
}

// Service implements the sign-in use cases.
type Service struct {
	users    Users
	verifier InitDataVerifier
	tokens   Tokens
	now      func() time.Time
	guards   Challenger
}

// New wires the use cases. now may be nil (time.Now).
func New(users Users, verifier InitDataVerifier, tokens Tokens, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{users: users, verifier: verifier, tokens: tokens, now: now}
}

// RequireChallenge makes Guest ask c before it creates an account.
func (s *Service) RequireChallenge(c Challenger) *Service {
	s.guards = c
	return s
}

// Guest creates an anonymous account for a website visitor and issues a session. A guest has
// no identity to verify, so the challenge (when configured) is the only barrier against scripts
// minting accounts to collect free translations. It returns ErrGuestsDisabled while an operator
// has stopped guests, without spending a challenge check or creating anything; when the switch
// cannot be read it fails closed with an error that is not ErrGuestsDisabled.
func (s *Service) Guest(ctx context.Context, challengeResponse, ip string) (Session, error) {
	off, err := s.users.GuestsOff(ctx)
	if err != nil {
		return Session{}, fmt.Errorf("read guest switch: %w", err)
	}
	if off {
		return Session{}, ErrGuestsDisabled
	}
	if s.guards != nil {
		if err := s.guards.Check(ctx, challengeResponse, ip); err != nil {
			return Session{}, err
		}
	}
	u, err := s.users.CreateGuest(ctx)
	if err != nil {
		return Session{}, fmt.Errorf("create guest: %w", err)
	}
	return Session{Token: s.tokens.Issue(u.ID, s.now()), ExpiresIn: s.tokens.TTL(u.ID), UserID: u.ID}, nil
}

// Login verifies Telegram init data, registers the user on first launch and issues a session.
func (s *Service) Login(ctx context.Context, initData string) (Session, error) {
	now := s.now()
	id, err := s.verifier.Verify(initData, now)
	if err != nil {
		return Session{}, fmt.Errorf("%w: %w", ErrInvalidCredentials, err)
	}
	u, err := s.users.UpsertUser(ctx, id.UserID, trimSource(id.StartParam))
	if err != nil {
		return Session{}, fmt.Errorf("register user: %w", err)
	}
	if u.Banned {
		return Session{}, ErrBanned
	}
	return Session{
		Token:     s.tokens.Issue(u.ID, now),
		ExpiresIn: s.tokens.TTL(u.ID),
		UserID:    u.ID,
		FirstName: id.FirstName,
	}, nil
}

// Authenticate resolves a session token to a user who may still use the service.
func (s *Service) Authenticate(ctx context.Context, token string) (user.User, error) {
	id, err := s.tokens.Verify(token, s.now())
	if err != nil {
		return user.User{}, fmt.Errorf("%w: %w", ErrSessionInvalid, err)
	}
	u, err := s.users.GetUser(ctx, id)
	switch {
	case errors.Is(err, user.ErrNotFound):
		return user.User{}, ErrSessionInvalid
	case err != nil:
		return user.User{}, fmt.Errorf("load user: %w", err)
	case u.Banned:
		return user.User{}, ErrBanned
	}
	return u, nil
}

// maxSourceRunes bounds the stored start_param, like the campaign field of events.
const maxSourceRunes = 64

func trimSource(s string) string {
	if r := []rune(strings.TrimSpace(s)); len(r) > maxSourceRunes {
		return string(r[:maxSourceRunes])
	} else {
		return string(r)
	}
}
