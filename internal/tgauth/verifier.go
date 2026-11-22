package tgauth

import (
	"time"

	"github.com/dyudin0821/bestregards/internal/auth"
)

// Verifier adapts Validate to the auth.InitDataVerifier port.
type Verifier struct {
	BotToken string
	MaxAge   time.Duration
}

var _ auth.InitDataVerifier = Verifier{}

// Verify implements auth.InitDataVerifier.
func (v Verifier) Verify(raw string, now time.Time) (auth.Identity, error) {
	d, err := Validate(raw, v.BotToken, v.MaxAge, now)
	if err != nil {
		return auth.Identity{}, err
	}
	return auth.Identity{UserID: d.User.ID, FirstName: d.User.FirstName, StartParam: d.StartParam}, nil
}
