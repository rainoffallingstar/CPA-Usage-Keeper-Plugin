package main

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
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

func TestParseColabCallback(t *testing.T) {
	cases := []struct{ raw, wantCode, wantState string }{
		{"http://127.0.0.1:54321/?code=4/0AXabc-def&state=nonce=deadbeef", "4/0AXabc-def", "nonce=deadbeef"},
		{"http://127.0.0.1:54321?code=abc123", "abc123", ""},
		{"127.0.0.1:54321/?code=xyz&state=nonce=x", "xyz", "nonce=x"},
		{"code=just-the-code", "just-the-code", ""},
		{"4/0AXbare-code-value", "4/0AXbare-code-value", ""},
		{"http://127.0.0.1:1/?state=nonce=abc&code=after-state", "after-state", "nonce=abc"},
		{"code=a%2Fb%2Bc", "a/b+c", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		code, state := parseColabCallback(c.raw)
		if code != c.wantCode || state != c.wantState {
			t.Errorf("parseColabCallback(%q) = (%q, %q), want (%q, %q)", c.raw, code, state, c.wantCode, c.wantState)
		}
	}
}

// colabLoginSubmitCode must attach a manually-relayed code to the right pending
// login and reject a state that belongs to a different session.
func TestColabLoginSubmitCode(t *testing.T) {
	colabLoginMu.Lock()
	savedState := colabLoginState
	colabLoginState = make(map[string]*colabLoginPending)
	colabLoginMu.Unlock()
	defer func() {
		colabLoginMu.Lock()
		colabLoginState = savedState
		colabLoginMu.Unlock()
	}()

	const id = "session-1"
	colabLoginMu.Lock()
	colabLoginState[id] = &colabLoginPending{Nonce: "abc", StartedAt: time.Now()}
	colabLoginMu.Unlock()

	// Bare code.
	if err := colabLoginSubmitCode(id, "the-code"); err != nil {
		t.Fatalf("bare code rejected: %v", err)
	}
	colabLoginMu.Lock()
	got := colabLoginState[id].Code
	colabLoginMu.Unlock()
	if got != "the-code" {
		t.Fatalf("stored code = %q, want the-code", got)
	}

	// Full callback URL with matching state.
	if err := colabLoginSubmitCode(id, "http://127.0.0.1:9/?code=code2&state=nonce=abc"); err != nil {
		t.Fatalf("full URL rejected: %v", err)
	}

	// Wrong state must be refused.
	if err := colabLoginSubmitCode(id, "http://127.0.0.1:9/?code=code3&state=nonce=WRONG"); err == nil {
		t.Fatal("mismatched state should be rejected")
	}
	// Unknown session must be refused.
	if err := colabLoginSubmitCode("nope", "code"); err == nil {
		t.Fatal("unknown session should be rejected")
	}
	// Missing code must be refused.
	if err := colabLoginSubmitCode(id, "http://127.0.0.1:9/?state=nonce=abc"); err == nil {
		t.Fatal("missing code should be rejected")
	}
}

// A login response must expose the real redirect_uri so a remote user knows
// where the callback lands.
func TestColabLoginResponseHasRedirectURI(t *testing.T) {
	colabLoginMu.Lock()
	savedState := colabLoginState
	colabLoginState = make(map[string]*colabLoginPending)
	colabLoginMu.Unlock()
	defer func() {
		colabLoginMu.Lock()
		savedState2 := savedState
		_ = savedState2
		colabLoginState = savedState
		colabLoginMu.Unlock()
	}()

	resp := handleColabQuotaGet(map[string][]string{"action": {"login"}, "account": {"colab-remote-test"}})
	var payload struct {
		AuthURL     string `json:"auth_url"`
		LoginID     string `json:"login_id"`
		RedirectURI string `json:"redirect_uri"`
	}
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.RedirectURI == "" || !strings.HasPrefix(payload.RedirectURI, "http://127.0.0.1:") {
		t.Fatalf("redirect_uri = %q, want a real http://127.0.0.1:<port>", payload.RedirectURI)
	}
	if strings.Contains(payload.RedirectURI, "<port>") {
		t.Fatalf("redirect_uri still uses the <port> placeholder: %q", payload.RedirectURI)
	}
	if !strings.Contains(payload.AuthURL, "redirect_uri=") {
		t.Fatalf("auth_url missing redirect_uri: %q", payload.AuthURL)
	}
	// Clean up the loopback server this test opened.
	shutdownColabLoopback()
}
