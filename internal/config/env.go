package config

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// env reads variables and remembers every problem, so a misconfigured service reports all of
// them at once instead of one per restart.
type env struct {
	get      func(string) string
	problems []error
}

func (e *env) fail(format string, args ...any) {
	e.problems = append(e.problems, fmt.Errorf(format, args...))
}

func (e *env) err() error { return errors.Join(e.problems...) }

// str returns the variable, or fallback when it is empty.
func (e *env) str(name, fallback string) string {
	if v := e.get(name); v != "" {
		return v
	}
	return fallback
}

// flag is true only for "1".
func (e *env) flag(name string) bool { return e.get(name) == "1" }

// required returns the variable and reports it when empty.
func (e *env) required(name string) string {
	v := e.get(name)
	if v == "" {
		e.fail("%s is required", name)
	}
	return v
}

// token is a required credential that ends up inside a URL or a header. A stray newline, as from
// `echo secret > file`, would make an HTTP library fail with an error that quotes the whole URL,
// token included, so such a value is refused at startup instead.
func (e *env) token(name string) string {
	v := e.required(name)
	if strings.IndexFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		e.fail("%s must not contain whitespace or control characters (a trailing newline?)", name)
	}
	return v
}

// secret returns a secret that must be at least minLen characters; an empty one is a problem only
// when required.
func (e *env) secret(name string, minLen int, required bool) string {
	v := e.get(name)
	switch {
	case v == "" && required:
		e.fail("%s is required", name)
	case v != "" && len(v) < minLen:
		e.fail("%s must be at least %d characters", name, minLen)
	}
	return v
}

// integer returns the variable as a whole number in [lo, hi], or fallback when it is empty.
func (e *env) integer(name string, fallback, lo, hi int) int {
	v := e.get(name)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		e.fail("%s %q must be a whole number from %d to %d", name, v, lo, hi)
		return fallback
	}
	return n
}

// integer32 is integer for values that must fit an int32.
func (e *env) integer32(name string, fallback, lo, hi int32) int32 {
	v := e.get(name)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil || n < int64(lo) || n > int64(hi) {
		e.fail("%s %q must be a whole number from %d to %d", name, v, lo, hi)
		return fallback
	}
	return int32(n)
}

// number returns the variable as a finite number that is not negative, or fallback when empty.
func (e *env) number(name string, fallback float64) float64 {
	v := e.get(name)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n < 0 || math.IsNaN(n) || math.IsInf(n, 0) {
		e.fail("%s %q must be a non-negative number", name, v)
		return fallback
	}
	return n
}

// telegramIDs parses a comma-separated list of Telegram user ids.
func (e *env) telegramIDs(name string) []int64 {
	var ids []int64
	for _, part := range strings.Split(e.get(name), ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			e.fail("%s entry %q must be a positive Telegram id", name, part)
			continue
		}
		ids = append(ids, id)
	}
	return ids
}
