package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestModelNormalizationFixture is the Go half of the shared golden vectors.
// dashboard/model_normalization.test.js asserts the same fixture against the
// dashboard's normalizeModelName, so the UI's model grouping and the backend's
// price lookup cannot drift apart (that drift caused both the >100% cache rate
// and the "$0 cost" bugs).
func TestModelNormalizationFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "model_normalization.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture struct {
		Cases []struct {
			Model string `json:"model"`
			Base  string `json:"base"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("fixture has no cases")
	}

	for _, tc := range fixture.Cases {
		if got := normalizePriceModel(tc.Model); got != tc.Base {
			t.Errorf("normalizePriceModel(%q) = %q, want %q", tc.Model, got, tc.Base)
		}
	}

	// And the normalised base must actually resolve to a price: seed one price
	// per distinct base and require every variant to find it.
	bases := map[string]modelPrice{}
	for _, tc := range fixture.Cases {
		bases[tc.Base] = modelPrice{Prompt: 1, Completion: 2, Cache: 0.1}
	}
	setTestPrices(bases)
	defer setTestPrices(nil)

	for _, tc := range fixture.Cases {
		price, key, ok := matchPriceDetailedWithPreview(tc.Model)
		if !ok {
			t.Errorf("%q did not resolve to any price", tc.Model)
			continue
		}
		if normalizePriceModel(key) != tc.Base {
			t.Errorf("%q matched key %q which normalises to %q, want %q",
				tc.Model, key, normalizePriceModel(key), tc.Base)
		}
		if price.Prompt != 1 {
			t.Errorf("%q matched unexpected price %+v", tc.Model, price)
		}
	}
}
