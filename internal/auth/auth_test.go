package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dyudin0821/bestregards/internal/user"
)

var now = time.Unix(1_800_000_000, 0)

type fakeUsers struct {
	users      map[int64]user.User
	err        error
	lastSource string
	guestsOff  bool
	offErr     error
}

func (f *fakeUsers) GuestsOff(context.Context) (bool, error) { return f.guestsOff, f.offErr }

func (f *fakeUsers) UpsertUser(_ context.Context, id int64, source string) (user.User, error) {
	f.lastSource = source
	if f.err != nil {
		return user.User{}, f.err
	}
	u, ok := f.users[id]
	if !ok {
		u = user.User{ID: id, Source: source, CreatedAt: now}
		f.users[id] = u
	}
	return u, nil
}

func (f *fakeUsers) CreateGuest(context.Context) (user.User, error) {
	if f.err != nil {
		return user.User{}, f.err
	}
	u := user.User{ID: -int64(len(f.users) + 1), CreatedAt: now}
	f.users[u.ID] = u
	return u, nil
}

func (f *fakeUsers) GetUser(_ context.Context, id int64) (user.User, error) {
	if f.err != nil {
		return user.User{}, f.err
	}
	if u, ok := f.users[id]; ok {
		return u, nil
	}
	return user.User{}, user.ErrNotFound
}

type fakeVerifier struct {
	identity Identity
	err      error
}

func (f fakeVerifier) Verify(string, time.Time) (Identity, error) { return f.identity, f.err }

// fakeTokens maps token strings to user ids.
type fakeTokens map[string]int64

func (fakeTokens) Issue(int64, time.Time) string { return "issued" }
func (fakeTokens) TTL(int64) time.Duration       { return time.Hour }
func (f fakeTokens) Verify(token string, _ time.Time) (int64, error) {
	if id, ok := f[token]; ok {
		return id, nil
	}
	return 0, errors.New("bad token")
}

func newService(v fakeVerifier, users *fakeUsers) *Service {
	return New(users, v, fakeTokens{"valid": 42, "banned": 7}, func() time.Time { return now })
}

func TestLoginRegistersUserAndIssuesSession(t *testing.T) {
	users := &fakeUsers{users: map[int64]user.User{}}
	s := newService(fakeVerifier{identity: Identity{UserID: 42, FirstName: "Анна", StartParam: "tt_launch"}}, users)

	got, err := s.Login(context.Background(), "raw")
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != 42 || got.FirstName != "Анна" || got.Token == "" || got.ExpiresIn != time.Hour {
		t.Fatalf("unexpected session %+v", got)
	}
	if users.users[42].Source != "tt_launch" {
		t.Errorf("source not recorded: %+v", users.users[42])
	}
}

func TestLoginFailures(t *testing.T) {
	boom := errors.New("db down")
	cases := map[string]struct {
		verifier fakeVerifier
		users    *fakeUsers
		want     error
	}{
		"bad init data": {fakeVerifier{err: errors.New("bad sig")}, &fakeUsers{users: map[int64]user.User{}}, ErrInvalidCredentials},
		"banned":        {fakeVerifier{identity: Identity{UserID: 7}}, &fakeUsers{users: map[int64]user.User{7: {ID: 7, Banned: true}}}, ErrBanned},
		"storage error": {fakeVerifier{identity: Identity{UserID: 1}}, &fakeUsers{users: map[int64]user.User{}, err: boom}, boom},
	}
	for name, c := range cases {
		if _, err := newService(c.verifier, c.users).Login(context.Background(), "raw"); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}
}

func TestAuthenticate(t *testing.T) {
	users := &fakeUsers{users: map[int64]user.User{7: {ID: 7, Banned: true}, 42: {ID: 42}}}
	s := newService(fakeVerifier{}, users)

	if u, err := s.Authenticate(context.Background(), "valid"); err != nil || u.ID != 42 {
		t.Fatalf("valid token: %+v %v", u, err)
	}
	cases := map[string]struct {
		token string
		want  error
	}{
		"garbage token": {"nope", ErrSessionInvalid},
		"banned":        {"banned", ErrBanned},
	}
	for name, c := range cases {
		if _, err := s.Authenticate(context.Background(), c.token); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}
	delete(users.users, 42)
	if _, err := s.Authenticate(context.Background(), "valid"); !errors.Is(err, ErrSessionInvalid) {
		t.Errorf("deleted user: err = %v, want ErrSessionInvalid", err)
	}
}

func TestLoginStoresABoundedStartParam(t *testing.T) {
	users := &fakeUsers{users: map[int64]user.User{}}
	long := "tt_" + strings.Repeat("я", 300)
	s := newService(fakeVerifier{identity: Identity{UserID: 7, StartParam: "  " + long}}, users)
	if _, err := s.Login(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if got := []rune(users.lastSource); len(got) != maxSourceRunes || !strings.HasPrefix(users.lastSource, "tt_я") {
		t.Fatalf("stored %d runes: %q", len(got), users.lastSource)
	}
}

type fakeChallenge struct {
	err    error
	gotKey string
	gotIP  string
}

func (f *fakeChallenge) Check(_ context.Context, response, ip string) error {
	f.gotKey, f.gotIP = response, ip
	return f.err
}

func TestGuestCreatesAnAnonymousAccount(t *testing.T) {
	users := &fakeUsers{users: map[int64]user.User{}}
	got, err := newService(fakeVerifier{}, users).Guest(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID >= 0 || got.Token == "" || got.FirstName != "" || got.ExpiresIn != time.Hour {
		t.Fatalf("unexpected guest session %+v", got)
	}
	if len(users.users) != 1 {
		t.Fatalf("one guest must be stored, have %d", len(users.users))
	}
}

func TestGuestsSwitchedOffAreRefusedBeforeAnythingHappens(t *testing.T) {
	users := &fakeUsers{users: map[int64]user.User{}, guestsOff: true}
	c := &fakeChallenge{}
	s := newService(fakeVerifier{}, users).RequireChallenge(c)
	if _, err := s.Guest(context.Background(), "answer", "203.0.113.9"); !errors.Is(err, ErrGuestsDisabled) {
		t.Fatalf("err = %v, want ErrGuestsDisabled", err)
	}
	if len(users.users) != 0 || c.gotKey != "" {
		t.Fatalf("no account and no challenge call while guests are off: %d accounts, challenge saw %q", len(users.users), c.gotKey)
	}

	users.guestsOff, users.offErr = false, errors.New("db down")
	if _, err := s.Guest(context.Background(), "answer", ""); err == nil || errors.Is(err, ErrGuestsDisabled) {
		t.Fatalf("an unreadable switch fails closed with a plain error: %v", err)
	}
	if len(users.users) != 0 {
		t.Fatal("an unreadable switch must not create accounts")
	}

	users.offErr = nil
	if _, err := s.Guest(context.Background(), "answer", ""); err != nil {
		t.Fatalf("with guests back on: %v", err)
	}
}

func TestGuestAsksTheChallengeFirst(t *testing.T) {
	users := &fakeUsers{users: map[int64]user.User{}}
	c := &fakeChallenge{}
	s := newService(fakeVerifier{}, users).RequireChallenge(c)
	if _, err := s.Guest(context.Background(), "answer", "203.0.113.9"); err != nil {
		t.Fatal(err)
	}
	if c.gotKey != "answer" || c.gotIP != "203.0.113.9" {
		t.Fatalf("challenge saw %q from %q", c.gotKey, c.gotIP)
	}

	c.err = ErrChallengeFailed
	if _, err := s.Guest(context.Background(), "wrong", ""); !errors.Is(err, ErrChallengeFailed) {
		t.Fatalf("err = %v, want ErrChallengeFailed", err)
	}
	if len(users.users) != 1 {
		t.Fatalf("a failed challenge must not create an account, have %d", len(users.users))
	}
}
