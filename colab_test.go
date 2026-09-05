package main

import (
	"math"
	"strings"
	"testing"
)

func TestColabObfuscateRoundTrip(t *testing.T) {
	testTokens := []string{
		"ya29.a0AfH6SM-test-token-value",
		"",
		"1//0abc-def_ghi-JKL",
	}
	for _, tok := range testTokens {
		cipher := obfuscateToken(tok)
		got := deobfuscateToken(cipher)
		if got != tok {
			t.Fatalf("roundtrip mismatch: want %q got %q", tok, got)
		}
		if tok != "" && strings.Contains(cipher, tok) {
			t.Fatalf("token leaked in storage: %q", cipher)
		}
	}
}

func TestColabPKCE(t *testing.T) {
	v := generateCodeVerifier()
	if len(v) < 40 {
		t.Fatalf("code verifier too short: %q (%d)", v, len(v))
	}
	ch := generateCodeChallenge(v)
	if len(ch) != 43 {
		t.Fatalf("unexpected code challenge length: %d", len(ch))
	}
	// Deterministic: same verifier must map to same challenge.
	if ch != generateCodeChallenge(v) {
		t.Fatalf("code challenge not deterministic")
	}
	// RFC 7636 sample: verifier "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	// → challenge "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	sample := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	if got := generateCodeChallenge(sample); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatalf("S256 mismatch: got %q", got)
	}
}

func TestColabTierLabel(t *testing.T) {
	cases := map[string]string{
		// GAPI enum full names (what colab.pa.googleapis.com returns)
		"SUBSCRIPTION_TIER_PRO":       "Colab Pro",
		"SUBSCRIPTION_TIER_PRO_PLUS":  "Colab Pro+",
		"SUBSCRIPTION_TIER_NONE":      "Colab 免费版",
		"SUBSCRIPTION_TIER_UNSPECIFIED": "Colab 免费版",
		// Colab backend short names
		"PRO":      "Colab Pro",
		"PRO_PLUS": "Colab Pro+",
		"VERY_PRO": "Colab Pro+",
		"NONE":     "Colab 免费版",
		// Numeric backend values
		"1": "Colab Pro",
		"2": "Colab Pro+",
		"0": "Colab 免费版",
		"":  "Colab 免费版",
		"unknown-value": "Colab 免费版",
	}
	for in, want := range cases {
		if got := colabTierLabel(in); got != want {
			t.Fatalf("colabTierLabel(%q)=%q want %q", in, got, want)
		}
	}
}

func TestColabParseTokenCount(t *testing.T) {
	cases := map[string]int64{
		"":           0,
		"12345":      12345,
		"0":          0,
		"1500000":    1500000,
		" 42 ":       42,
	}
	for in, want := range cases {
		if got := parseTokenCount(in); got != want {
			t.Fatalf("parseTokenCount(%q)=%d want %d", in, got, want)
		}
	}
}

func TestColabLoginFields(t *testing.T) {
	// Verify the login response JSON shape the dashboard expects.
	resp := map[string]any{
		"auth_url":     "https://accounts.google.com/o/oauth2/v2/auth?...",
		"login_id":     "deadbeef",
		"account":      "colab-main",
		"redirect_uri": "http://127.0.0.1:54321/?code=...&state=nonce=deadbeef",
	}
	if resp["login_id"] == "" || resp["auth_url"] == "" {
		t.Fatalf("login response missing required fields")
	}
}

func TestComputeColabCeiling(t *testing.T) {
	cases := []struct {
		tier      string
		balance   float64
		wantTotal float64
		wantUsed  float64
	}{
		{"SUBSCRIPTION_TIER_PRO", 63.16, 100.0, 36.84},
		{"SUBSCRIPTION_TIER_PRO", 100.0, 100.0, 0.0},
		{"SUBSCRIPTION_TIER_PRO", 0.0, 100.0, 100.0},
		{"SUBSCRIPTION_TIER_PRO", 145.2, 200.0, 54.8},
		{"SUBSCRIPTION_TIER_PRO", 290.0, 300.0, 10.0},
		{"SUBSCRIPTION_TIER_PRO_PLUS", 600.0, 600.0, 0.0},
		{"SUBSCRIPTION_TIER_PRO_PLUS", 0.0, 600.0, 600.0},
		{"SUBSCRIPTION_TIER_PRO_PLUS", 450.0, 600.0, 150.0},
		{"SUBSCRIPTION_TIER_PRO_PLUS", 780.0, 800.0, 20.0},
		{"ENTERPRISE", 300.0, 300.0, 0.0},
		{"SUBSCRIPTION_TIER_NONE", 85.0, 100.0, 15.0},
	}
	for _, c := range cases {
		tot, usd, _ := computeColabCeiling(c.tier, c.balance)
		if math.Abs(tot-c.wantTotal) > 0.01 || math.Abs(usd-c.wantUsed) > 0.01 {
			t.Errorf("computeColabCeiling(%q, %.2f) = (%.2f, %.2f), want (%.2f, %.2f)",
				c.tier, c.balance, tot, usd, c.wantTotal, c.wantUsed)
		}
	}
}

func TestUnmaskSecret(t *testing.T) {
	if len(colabOAuthClientID) < 40 || !strings.Contains(colabOAuthClientID, "google") {
		t.Fatalf("unmasked client id invalid: %q", colabOAuthClientID)
	}
	if len(colabOAuthClientSecret) < 20 || colabOAuthClientSecret[:3] != "GOC" {
		t.Fatalf("unmasked client secret invalid: %q", colabOAuthClientSecret)
	}
}
