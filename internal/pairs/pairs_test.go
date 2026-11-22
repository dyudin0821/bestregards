package pairs

import "testing"

func TestCatalogHasFortyUniquePairsAcrossModes(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Pairs) != 40 {
		t.Fatalf("pairs = %d, want 40", len(c.Pairs))
	}
	want := map[string]int{"hr": 10, "colleagues": 8, "boss": 6, "reverse": 16}
	got := map[string]int{}
	for _, p := range c.Pairs {
		got[p.Mode]++
	}
	for mode, n := range want {
		if got[mode] != n {
			t.Errorf("mode %s: %d pairs, want %d", mode, got[mode], n)
		}
	}
}

func TestFindMatchesEveryPairInItsModeOnly(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range c.Pairs {
		found, ok := c.Find(p.Mode, p.Input)
		if !ok || found.ID != p.ID {
			t.Errorf("pair %d not found by its own input", p.ID)
		}
		if _, ok := c.Find("no-such-mode", p.Input); ok {
			t.Errorf("pair %d matched an unknown mode", p.ID)
		}
	}
}

func TestNormalizeIgnoresCaseQuotesPunctuationAndSpacing(t *testing.T) {
	if a, b := Normalize("  «Привет,   МИР!!» "), Normalize("привет, мир"); a != b {
		t.Fatalf("%q != %q", a, b)
	}
}

func TestValidateRejectsBrokenCatalogs(t *testing.T) {
	modes := []Mode{{ID: "hr", Label: "С HR"}}
	cases := map[string]Catalog{
		"duplicate id": {Modes: modes, Pairs: []Pair{{ID: 1, Mode: "hr", Title: "a", Input: "b", Output: "c"}, {ID: 1, Mode: "hr", Title: "a", Input: "b", Output: "c"}}},
		"unknown mode": {Modes: modes, Pairs: []Pair{{ID: 1, Mode: "x", Title: "a", Input: "b", Output: "c"}}},
		"empty output": {Modes: modes, Pairs: []Pair{{ID: 1, Mode: "hr", Title: "a", Input: "b", Output: " "}}},
	}
	for name, c := range cases {
		if err := c.validate(); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
