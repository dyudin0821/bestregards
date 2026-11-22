package tgauth

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

const token = "123456:TEST-TOKEN"

var now = time.Unix(1_800_000_000, 0)

func fields(age time.Duration) map[string]string {
	return map[string]string{
		"auth_date":   strconv.FormatInt(now.Add(-age).Unix(), 10),
		"query_id":    "AAH",
		"start_param": "tt_launch",
		"user":        `{"id":42,"first_name":"Анна","username":"anna"}`,
	}
}

func TestValidateAcceptsSignedData(t *testing.T) {
	d, err := Validate(Sign(fields(time.Minute), token), token, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if d.User.ID != 42 || d.User.FirstName != "Анна" || d.StartParam != "tt_launch" {
		t.Fatalf("unexpected data: %+v", d)
	}
}

// The vector was computed independently with Python's hmac module following Telegram's documentation.
func TestValidateKnownVector(t *testing.T) {
	const raw = "auth_date=1799999940&query_id=AAH&start_param=tt_launch&user=%7B%22id%22%3A42%2C%22first_name%22%3A%22%D0%90%D0%BD%D0%BD%D0%B0%22%2C%22username%22%3A%22anna%22%7D&hash=21967d8367c20e9ac026067f22e248a1821f97075cf235e2c4766451b465cea5"
	if _, err := Validate(raw, token, time.Hour, now); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejects(t *testing.T) {
	signed := Sign(fields(time.Minute), token)
	tampered := strings.Replace(signed, "42", "43", 1)
	noUser := fields(time.Minute)
	delete(noUser, "user")
	guestRange := fields(time.Minute)
	guestRange["user"] = `{"id":-7,"first_name":"x"}` // negative ids are the guests' range

	cases := map[string]string{
		"wrong bot token": Sign(fields(time.Minute), "999:OTHER"),
		"tampered field":  tampered,
		"expired":         Sign(fields(2*time.Hour), token),
		"from the future": Sign(fields(-time.Hour), token),
		"no user":         Sign(noUser, token),
		"guest id range":  Sign(guestRange, token),
		"no hash":         "auth_date=1&user=%7B%7D",
		"bad hash":        "hash=zz&auth_date=1",
		"empty":           "",
	}
	for name, raw := range cases {
		if _, err := Validate(raw, token, time.Hour, now); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}
