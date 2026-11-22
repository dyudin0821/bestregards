// Package limits holds the pure rules that decide how much a user may translate for free and
// how much the service may spend on free translations per day. No I/O, no clock: callers pass
// the time and the location in which days are counted.
package limits

import (
	"math"
	"strings"
	"time"

	"github.com/dyudin0821/bestregards/internal/user"
)

const (
	// FreeDaily is the number of free AI translations per user per day.
	FreeDaily = 5
	// GuestDaily is the allowance of an anonymous website visitor, lower than for Telegram users so
	// that signing in through the Mini App is worth it.
	GuestDaily = 3
	// AdFirstDay is the smaller allowance on the first day for users who came from an ad.
	AdFirstDay = 3
	// AdSourcePrefix marks start_param values of paid campaigns (e.g. "tt_launch").
	AdSourcePrefix = "tt_"
	// GuestBudgetShare is the share of the hourly cap that all guests together may spend.
	GuestBudgetShare = 0.4

	hoursPerDay = 24
)

// Day returns the calendar date of now in loc as midnight UTC, a stable key for per-day counters.
func Day(now time.Time, loc *time.Location) time.Time {
	y, m, d := now.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// ResetsAt is the moment the next day starts in loc, when free quotas renew.
func ResetsAt(now time.Time, loc *time.Location) time.Time {
	y, m, d := now.In(loc).Date()
	return time.Date(y, m, d+1, 0, 0, 0, 0, loc)
}

// DailyLimit is how many free AI translations u may make on the day containing now.
func DailyLimit(u user.User, now time.Time, loc *time.Location) int {
	if u.Guest() {
		return GuestDaily
	}
	if strings.HasPrefix(u.Source, AdSourcePrefix) && Day(u.CreatedAt, loc).Equal(Day(now, loc)) {
		return AdFirstDay
	}
	return FreeDaily
}

// BudgetCap is the most the free translations may have spent by now. The daily ceiling is spread
// evenly over the 24 hours and unused hours carry over, so a burst of visitors in the morning
// cannot use up the whole day.
func BudgetCap(ceilingMicro int64, now time.Time, loc *time.Location) int64 {
	hour := now.In(loc).Hour()
	return ceilingMicro * int64(hour+1) / hoursPerDay
}

// GuestBudgetCap is the part of BudgetCap that anonymous guests may spend. Anyone who passes the
// challenge becomes a guest, so without this sub-ceiling a script could take every hourly slice
// and leave Telegram users with nothing; they may always use the whole remaining cap.
func GuestBudgetCap(ceilingMicro int64, now time.Time, loc *time.Location) int64 {
	return int64(math.Round(float64(BudgetCap(ceilingMicro, now, loc)) * GuestBudgetShare))
}

// Prices are provider prices in US dollars per one million tokens.
type Prices struct {
	InputPerMtok  float64
	OutputPerMtok float64
}

// CostMicro converts token usage to millionths of a dollar, rounding up. One token costs
// exactly "price per million tokens" micro-dollars.
func (p Prices) CostMicro(inputTokens, outputTokens int) int64 {
	return int64(math.Ceil(float64(inputTokens)*p.InputPerMtok + float64(outputTokens)*p.OutputPerMtok))
}
