// Package pairs holds the curated, human-reviewed translation pairs shipped inside the binary.
package pairs

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed pairs.json
var raw []byte

// Mode is a communication context a translation is made for.
type Mode struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Pair is a ready-made "original -> translation" example that needs no AI.
type Pair struct {
	ID     int    `json:"id"`
	Mode   string `json:"mode"`
	Title  string `json:"title"`
	Input  string `json:"input"`
	Output string `json:"output"`
}

// Catalog is the full set of modes and pairs.
type Catalog struct {
	Modes []Mode `json:"modes"`
	Pairs []Pair `json:"pairs"`
}

// Load parses and validates the embedded catalog.
func Load() (*Catalog, error) {
	var c Catalog
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse pairs.json: %w", err)
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Catalog) validate() error {
	modes := make(map[string]bool, len(c.Modes))
	for _, m := range c.Modes {
		if m.ID == "" || m.Label == "" {
			return fmt.Errorf("mode %+v: id and label are required", m)
		}
		modes[m.ID] = true
	}
	ids := make(map[int]bool, len(c.Pairs))
	for _, p := range c.Pairs {
		switch {
		case ids[p.ID]:
			return fmt.Errorf("pair %d: duplicate id", p.ID)
		case !modes[p.Mode]:
			return fmt.Errorf("pair %d: unknown mode %q", p.ID, p.Mode)
		case strings.TrimSpace(p.Input) == "" || strings.TrimSpace(p.Output) == "" || strings.TrimSpace(p.Title) == "":
			return fmt.Errorf("pair %d: title, input and output are required", p.ID)
		}
		ids[p.ID] = true
	}
	return nil
}

// Normalize makes two texts comparable regardless of case, quotes, trailing punctuation and spacing.
func Normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("«", "", "»", "", "“", "", "”", "", `"`, "").Replace(s)
	s = strings.TrimRight(s, ".!?")
	return strings.Join(strings.Fields(s), " ")
}

// Find returns the ready-made pair for the given mode whose original matches text.
func (c *Catalog) Find(mode, text string) (Pair, bool) {
	want := Normalize(text)
	for _, p := range c.Pairs {
		if p.Mode == mode && Normalize(p.Input) == want {
			return p, true
		}
	}
	return Pair{}, false
}
