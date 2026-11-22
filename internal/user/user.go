// Package user is the innermost layer: the entity every other layer talks about.
// It imports nothing from this project and no framework.
package user

import (
	"errors"
	"time"
)

// ErrNotFound is returned by repositories when no such user exists.
var ErrNotFound = errors.New("user not found")

// User is a registered person: a Telegram user or an anonymous guest of the website. Names and
// texts are never kept.
type User struct {
	ID         int64  // the Telegram id, or a negative number for a guest
	Source     string // start_param of the first launch, for campaign attribution
	Banned     bool
	CreatedAt  time.Time
	LastSeenAt time.Time
}

// IsGuest reports whether id belongs to an anonymous website visitor. Telegram ids are positive,
// guests get negative ones, so both live in the same tables without a clash.
func IsGuest(id int64) bool { return id < 0 }

// Guest reports whether the user is an anonymous website visitor, who cannot buy packs.
func (u User) Guest() bool { return IsGuest(u.ID) }
