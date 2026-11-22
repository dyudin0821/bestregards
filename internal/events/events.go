// Package events records anonymous product events for the "was the translation used" metric.
// An event never carries a text. A user is represented only by a keyed hash, so the table cannot
// be joined back to Telegram ids without the secret pepper.
package events

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"regexp"
	"strings"
	"time"
)

// Type is what happened.
type Type string

const (
	TranslateDone   Type = "translate_done"
	Copied          Type = "copied"
	QuotaExhausted  Type = "quota_exhausted"
	BudgetExhausted Type = "budget_exhausted"
)

// Kind tells where a translation came from.
type Kind string

const (
	KindNone Kind = ""
	KindPair Kind = "pair"
	KindAI   Kind = "ai"
)

// Source is the channel the user came through.
type Source string

const (
	SourceMiniApp Source = "miniapp"
	SourceWeb     Source = "web"
	SourceInline  Source = "inline"
)

// ClientTypes are the only event types a client may report itself. The rest are recorded by the
// server, where they cannot be forged.
var ClientTypes = map[Type]bool{TranslateDone: true, Copied: true}

// MaxCampaignLen bounds the stored start_param.
const MaxCampaignLen = 64

// Event is what the use cases report. UserID 0 means an anonymous visitor.
type Event struct {
	Type     Type
	Kind     Kind
	Mode     string
	Level    int
	Source   Source
	ResultID string
	Campaign string
	UserID   int64
}

// Recorder takes events. Implementations must never block the caller and never fail it.
type Recorder interface {
	Record(Event)
}

type discard struct{}

func (discard) Record(Event) {}

// Discard drops every event.
var Discard Recorder = discard{}

// Row is an event ready to be stored.
type Row struct {
	At       time.Time
	Type     Type
	Kind     Kind
	Mode     string
	Level    int
	Source   Source
	ResultID string
	UserHash string
	Campaign string
}

// UserHash is the keyed hash that stands in for a Telegram id. Empty for anonymous visitors.
func UserHash(pepper []byte, userID int64) string {
	if userID == 0 {
		return ""
	}
	var id [8]byte
	binary.BigEndian.PutUint64(id[:], uint64(userID))
	m := hmac.New(sha256.New, pepper)
	m.Write(id[:])
	return hex.EncodeToString(m.Sum(nil))[:32]
}

// NewResultID returns a random identifier that ties a translation to the later "copied" event.
func NewResultID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

var resultIDPattern = regexp.MustCompile(`^[a-z0-9]{8,32}$`)

// ValidResultID reports whether id has the shape of an identifier from NewResultID or the client.
func ValidResultID(id string) bool { return resultIDPattern.MatchString(id) }

// TrimCampaign shortens a start_param to what is stored.
func TrimCampaign(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > MaxCampaignLen {
		return string(r[:MaxCampaignLen])
	}
	return s
}
