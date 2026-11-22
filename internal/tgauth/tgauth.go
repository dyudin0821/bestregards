// Package tgauth validates Telegram Mini App init data.
package tgauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrInvalid is returned for any init data that must not be trusted.
var ErrInvalid = errors.New("invalid init data")

// User is the subset of Telegram's WebAppUser the service uses. Names are never persisted.
type User struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

// InitData is verified Mini App launch data.
type InitData struct {
	User       User
	AuthDate   time.Time
	StartParam string
}

// Validate checks the HMAC signature of raw (Telegram.WebApp.initData) against botToken
// and rejects data older than maxAge.
func Validate(raw, botToken string, maxAge time.Duration, now time.Time) (*InitData, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	hash := values.Get("hash")
	if hash == "" {
		return nil, fmt.Errorf("%w: missing hash", ErrInvalid)
	}
	got, err := hex.DecodeString(hash)
	if err != nil {
		return nil, fmt.Errorf("%w: malformed hash", ErrInvalid)
	}
	values.Del("hash")

	pairs := make([]string, 0, len(values))
	for k, v := range values {
		pairs = append(pairs, k+"="+v[0])
	}
	sort.Strings(pairs)

	secret := hmacSHA256([]byte("WebAppData"), []byte(botToken))
	if !hmac.Equal(got, hmacSHA256(secret, []byte(strings.Join(pairs, "\n")))) {
		return nil, fmt.Errorf("%w: signature mismatch", ErrInvalid)
	}

	authUnix, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%w: bad auth_date", ErrInvalid)
	}
	authDate := time.Unix(authUnix, 0)
	if age := now.Sub(authDate); age > maxAge || age < -time.Minute {
		return nil, fmt.Errorf("%w: expired", ErrInvalid)
	}

	data := &InitData{AuthDate: authDate, StartParam: values.Get("start_param")}
	if err := json.Unmarshal([]byte(values.Get("user")), &data.User); err != nil || data.User.ID <= 0 {
		return nil, fmt.Errorf("%w: missing user", ErrInvalid)
	}
	return data, nil
}

// Sign produces init data signed the way Telegram does. It exists for tests and local tooling.
func Sign(fields map[string]string, botToken string) string {
	values := url.Values{}
	pairs := make([]string, 0, len(fields))
	for k, v := range fields {
		values.Set(k, v)
		pairs = append(pairs, k+"="+v)
	}
	sort.Strings(pairs)
	secret := hmacSHA256([]byte("WebAppData"), []byte(botToken))
	values.Set("hash", hex.EncodeToString(hmacSHA256(secret, []byte(strings.Join(pairs, "\n")))))
	return values.Encode()
}

func hmacSHA256(key, msg []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(msg)
	return m.Sum(nil)
}
