// Package turnstile checks Cloudflare Turnstile answers, the invisible challenge that tells a
// browser with a person behind it from a script.
package turnstile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dyudin0821/bestregards/internal/auth"
)

// VerifyURL is where Cloudflare validates answers.
const VerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

const (
	timeout     = 5 * time.Second
	maxResponse = 2048 // Cloudflare's own limit for an answer is 2048 characters
	maxBody     = 64 << 10
)

var _ auth.Challenger = (*Verifier)(nil)

// Verifier validates answers with the secret key of the site.
type Verifier struct {
	Secret string
	URL    string       // empty means VerifyURL
	HTTP   *http.Client // nil means a client with the package timeout
}

// Check implements auth.Challenger. An answer is good once: Cloudflare rejects a repeat.
func (v *Verifier) Check(ctx context.Context, response, ip string) error {
	if response == "" || len(response) > maxResponse {
		return fmt.Errorf("%w: no answer", auth.ErrChallengeFailed)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	form := url.Values{"secret": {v.Secret}, "response": {response}}
	if ip != "" {
		form.Set("remoteip", ip)
	}
	target := v.URL
	if target == "" {
		target = VerifyURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := v.HTTP
	if client == nil {
		client = &http.Client{}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("call turnstile: %w", errors.Unwrap(err)) // the URL is not a secret, but the form is not worth echoing
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody)) // drained so the connection can be reused
		return fmt.Errorf("turnstile returned status %d", resp.StatusCode)
	}
	var out struct {
		Success bool     `json:"success"`
		Errors  []string `json:"error-codes"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&out); err != nil {
		return fmt.Errorf("decode turnstile answer: %w", err)
	}
	if out.Success {
		return nil
	}
	for _, code := range out.Errors {
		// These mean our own setup is wrong, not that the visitor failed.
		if code == "missing-input-secret" || code == "invalid-input-secret" {
			return fmt.Errorf("turnstile rejected our secret: %s", code)
		}
	}
	return fmt.Errorf("%w: %s", auth.ErrChallengeFailed, strings.Join(out.Errors, ","))
}
