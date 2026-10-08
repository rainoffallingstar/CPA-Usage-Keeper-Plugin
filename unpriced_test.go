package main

import (
	"testing"
	"time"
)

// Client-prefixed model names (a provider/desktop-app marker such as
// "kiro-") refer to the same underlying model, so they must resolve to that
// model's price instead of costing $0.
func TestMatchPriceResolvesClientPrefixedModels(t *testing.T) {
	setTestPrices(map[string]modelPrice{
		"claude-opus-4-5":   {Prompt: 5, Completion: 25, Cache: 0.5},
		"claude-sonnet-4-5": {Prompt: 3, Completion: 15, Cache: 0.3},
		"gpt-5-6-terra":     {Prompt: 2, Completion: 12, Cache: 0.2},
	})
	defer setTestPrices(nil)

	cases := []struct{ model, wantKey string }{
		{"kiro-claude-opus-4-5", "claude-opus-4-5"},
		{"kiro-claude-sonnet-4-5", "claude-sonnet-4-5"},
		{"some-client-gpt-5.6-terra", "gpt-5-6-terra"},
		// Exact matches must still win over the prefix fallback.
		{"claude-opus-4-5", "claude-opus-4-5"},
	}
	for _, tc := range cases {
		p, key, ok := matchPriceDetailedWithPreview(tc.model)
		if !ok {
			t.Errorf("no price matched for %q", tc.model)
			continue
		}
		if key != tc.wantKey {
			t.Errorf("%q matched %q, want %q", tc.model, key, tc.wantKey)
		}
		if p.Prompt == 0 {
			t.Errorf("%q matched a zero price", tc.model)
		}
	}

	// An unknown prefix must not invent a match for a name with no known tail.
	if _, _, ok := matchPriceDetailedWithPreview("kiro-totally-unknown-model"); ok {
		t.Error("unknown model should not resolve")
	}
}

// A model seen only in zero-token requests costs $0 whether or not it is
// priced, so it must not count towards the "no price" alert (a single failed
// probe request used to raise a permanent warning).
func TestCountUnpricedModelsIgnoresZeroTokenModels(t *testing.T) {
	d, cleanup := setupTestDB(t)
	defer cleanup()
	setTestPrices(map[string]modelPrice{"priced-model": {Prompt: 1, Completion: 2, Cache: 0.1}})
	defer setTestPrices(nil)

	now := time.Now().Format(time.RFC3339)
	// Failed probe requests: no tokens consumed.
	insertTestEvent(t, d, "kiro", "kiro-claude-opus-4-5", 0, 0, 0, true, now)
	insertTestEvent(t, d, "kiro", "kiro-claude-sonnet-4-5", 0, 0, 0, true, now)
	// A genuinely unpriced model that did consume tokens.
	insertTestEvent(t, d, "p", "mystery-model", 900, 100, 1000, false, now)
	// A priced model with tokens must not count either.
	insertTestEvent(t, d, "p", "priced-model", 500, 500, 1000, false, now)

	if got := countUnpricedModels(); got != 1 {
		t.Errorf("countUnpricedModels() = %d, want 1 (only mystery-model consumed tokens without a price)", got)
	}
}
