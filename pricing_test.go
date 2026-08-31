package main

import (
	"math"
	"testing"
)

func setTestPrices(prices map[string]modelPrice) {
	pricesMu.Lock()
	defer pricesMu.Unlock()
	pricesStore = make(map[string]modelPrice)
	for model, price := range prices {
		pricesStore[model] = price
	}
}

func TestModelVariants(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  map[string]bool
	}{
		{
			name:  "colon variant",
			model: "deepseek-v4-pro:0813",
			want: map[string]bool{
				"deepseek-v4-pro-0813": true,
				"deepseek-v4-pro:0813": false, // never include the input itself
				"deepseekv4pro0813":    true,
			},
		},
		{
			name:  "dot variant",
			model: "deepseek.v4.pro",
			want: map[string]bool{
				"deepseek-v4-pro": true,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			variants := modelVariants(tt.model)
			for variant, expected := range tt.want {
				got := containsString(variants, variant)
				if got != expected {
					t.Errorf("modelVariants(%q) contains %q = %v, want %v (variants: %v)", tt.model, variant, got, expected, variants)
				}
			}
		})
	}
}

func containsString(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}

func TestStripVariantSuffix(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{model: "claude-opus-4-6-thinking", want: "claude-opus-4-6"},
		{model: "claude-opus-4-6-thinking-pro", want: "claude-opus-4-6"},
		{model: "gemini-3-7-flash-high", want: "gemini-3-7-flash"},
		{model: "deepseek-v4-pro:preview", want: "deepseek-v4-pro"},
		{model: "deepseek-v4-pro:0813", want: "deepseek-v4-pro"},
		{model: "deepseek-v4-flash:0731", want: "deepseek-v4-flash"},
		{model: "gemini-3-7-flash", want: "gemini-3-7-flash"},
		{model: "deepseek-v4-pro-0813", want: "deepseek-v4-pro"},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := stripVariantSuffix(tt.model)
			if got != tt.want {
				t.Errorf("stripVariantSuffix(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

func TestMatchPrice(t *testing.T) {
	setTestPrices(map[string]modelPrice{
		"deepseek-v4-pro":        {Prompt: 0.66, Completion: 1.98, Cache: 0.022, AutoSynced: true},
		"deepseek-v4-flash":      {Prompt: 0.0786, Completion: 0.1572, Cache: 0.0157, AutoSynced: true},
		"deepseek-v4-pro-0813":   {Prompt: 0.66, Completion: 1.98, Cache: 0.022, AutoSynced: true},
		"deepseek-v4-flash-0731": {Prompt: 0.14, Completion: 0.28, Cache: 0.028, AutoSynced: true},
		"gemini-3-7-flash":       {Prompt: 0.375, Completion: 1.875, Cache: 0.0375, AutoSynced: true},
		"claude-opus-4-6":        {Prompt: 5.0, Completion: 25.0, Cache: 0.5, AutoSynced: true},
	})
	defer setTestPrices(nil)

	tests := []struct {
		model     string
		wantMatch bool
		wantKey   string
	}{
		{model: "claude-opus-4-6", wantMatch: true, wantKey: "claude-opus-4-6"},
		// Exact variant is not in the store; must fall back to the base model.
		{model: "claude-opus-4-6-thinking", wantMatch: true, wantKey: "claude-opus-4-6"},
		// Colon vs dash spelling.
		{model: "deepseek-v4-pro:0813", wantMatch: true, wantKey: "deepseek-v4-pro-0813"},
		{model: "deepseek-v4-flash:0731", wantMatch: true, wantKey: "deepseek-v4-flash-0731"},
		// Suffix variants that only exist as the base model.
		{model: "gemini-3-7-flash-high", wantMatch: true, wantKey: "gemini-3-7-flash"},
		{model: "deepseek-v4-pro:preview", wantMatch: true, wantKey: "deepseek-v4-pro"},
		// Unrelated model with no entry.
		{model: "gpt-5", wantMatch: false, wantKey: ""},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			price, key, ok := matchPriceDetailed(tt.model)
			if ok != tt.wantMatch {
				t.Fatalf("matchPriceDetailed(%q) ok = %v, want %v", tt.model, ok, tt.wantMatch)
			}
			if !tt.wantMatch {
				return
			}
			if key != tt.wantKey {
				t.Errorf("matchPriceDetailed(%q) key = %q, want %q", tt.model, key, tt.wantKey)
			}
			if price.Prompt <= 0 {
				t.Errorf("matchPriceDetailed(%q) prompt = %v, want > 0", tt.model, price.Prompt)
			}
		})
	}
}

func TestComputeCost(t *testing.T) {
	setTestPrices(map[string]modelPrice{
		"claude-opus-4-6": {Prompt: 5.0, Completion: 25.0, Cache: 0.5, AutoSynced: true},
	})
	defer setTestPrices(nil)

	cost := computeCost("claude-opus-4-6-thinking", 100000, 20000, 40000)
	// 60000 non-cached input tokens * 5/1M + 20000 output * 25/1M + 40000 cached * 0.5/1M
	want := 0.06*5.0 + 0.02*25.0 + 0.04*0.5
	if math.Abs(cost-want) > 1e-9 {
		t.Errorf("computeCost() = %v, want %v", cost, want)
	}

	if cost := computeCost("unknown-model", 100, 100, 0); cost != 0 {
		t.Errorf("computeCost(unknown) = %v, want 0", cost)
	}
}
