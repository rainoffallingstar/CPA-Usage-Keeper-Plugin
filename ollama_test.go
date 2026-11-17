package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildOllamaCookieHeaderRawValue(t *testing.T) {
	got := buildOllamaCookieHeader("abc123")
	if got != "__Secure-session=abc123" {
		t.Errorf("buildOllamaCookieHeader(raw) = %q, want __Secure-session=abc123", got)
	}
}

func TestBuildOllamaCookieHeaderFullCookie(t *testing.T) {
	cookie := "aid=x; __Secure-session=token123"
	got := buildOllamaCookieHeader(cookie)
	if got != cookie {
		t.Errorf("buildOllamaCookieHeader(full) = %q, want %q", got, cookie)
	}
}

func TestBuildOllamaCookieHeaderCookiePrefix(t *testing.T) {
	got := buildOllamaCookieHeader("Cookie: aid=x; __Secure-session=token123")
	if got != "aid=x; __Secure-session=token123" {
		t.Errorf("buildOllamaCookieHeader(cookie-prefix) = %q", got)
	}
}

func TestParseOllamaQuotaHTML(t *testing.T) {
	html := `
    <span>Cloud usage</span>
    <span class="text-xs font-normal px-2 py-0.5 rounded-full bg-neutral-100 text-neutral-600 capitalize">pro</span>
    <div class="flex justify-between mb-2">
      <span class="text-sm text-neutral-400">Session usage</span>
      <span class="text-sm text-neutral-500">Weekly limit reached</span>
    </div>
    <div data-usage-track aria-label="Session usage 12.5% used"></div>
    <div class="local-time" data-time="2026-06-29T00:00:00Z">Sessions resume in 3 days.</div>
    <div class="flex justify-between mb-2">
      <span class="text-sm">Weekly usage</span>
      <span class="text-sm text-red-500">80% used</span>
    </div>
    <div data-usage-track aria-label="Weekly usage 80% used">
      <button data-usage-segment data-model="glm-5.2" data-requests="100" style="width: 80%; background: #3b82f6"></button>
    </div>
    <div class="local-time" data-time="2026-06-30T00:00:00Z">Resets in 4 days.</div>
    <script>
    `
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	plan, windows, err := parseOllamaQuotaHTML(html, now)
	if err != nil {
		t.Fatalf("parseOllamaQuotaHTML error = %v", err)
	}
	if plan != "pro" {
		t.Errorf("plan = %q, want pro", plan)
	}
	if len(windows) != 2 {
		t.Fatalf("len(windows) = %d, want 2", len(windows))
	}
	if windows[0].Label != "Session" {
		t.Errorf("windows[0].Label = %q, want Session", windows[0].Label)
	}
	if windows[0].Used != 12.5 {
		t.Errorf("windows[0].Used = %v, want 12.5", windows[0].Used)
	}
	if windows[0].StatusText != "Weekly limit reached" {
		t.Errorf("windows[0].StatusText = %q, want Weekly limit reached", windows[0].StatusText)
	}
	if windows[1].Used != 80.0 {
		t.Errorf("windows[1].Used = %v, want 80.0", windows[1].Used)
	}
	if len(windows[1].Models) == 0 || windows[1].Models[0].Model != "glm-5.2" {
		t.Errorf("windows[1].Models = %+v, want glm-5.2", windows[1].Models)
	}
	if windows[1].Models[0].Requests != 100 {
		t.Errorf("windows[1].Models[0].Requests = %d, want 100", windows[1].Models[0].Requests)
	}
	if windows[1].Models[0].SharePercent != 80.0 {
		t.Errorf("windows[1].Models[0].SharePercent = %v, want 80.0", windows[1].Models[0].SharePercent)
	}
}

func TestParseOllamaQuotaHTMLNotLoggedIn(t *testing.T) {
	html := `<html><body>Please sign in to continue</body></html>`
	now := time.Now().UTC()
	_, _, err := parseOllamaQuotaHTML(html, now)
	if err == nil {
		t.Fatal("expected error for not-logged-in page")
	}
}

func TestParseOllamaQuotaHTMLMissingBlock(t *testing.T) {
	html := `<html><body>some other page</body></html>`
	now := time.Now().UTC()
	_, _, err := parseOllamaQuotaHTML(html, now)
	if err == nil {
		t.Fatal("expected error for missing Cloud usage block")
	}
}

// A Chinese-locale browser (or a chat app relaying the value) rewrites the
// ASCII ";" separator as the full-width "；" (U+FF1B). ollama.com splits the
// Cookie header on ASCII ";" only, so the whole string collapsed into a single
// aid cookie, __Secure-session was never sent, and the dashboard reported
// "cookie 无效" for a cookie that was in fact perfectly valid.
func TestBuildOllamaCookieHeaderRepairsFullWidthPunctuation(t *testing.T) {
	got := buildOllamaCookieHeader("aid=46ecb933-35bc-4c97-a651-090bfd36ea89；__Secure-session=YWdlLWVuY3J5cHRpb24=")
	want := "aid=46ecb933-35bc-4c97-a651-090bfd36ea89; __Secure-session=YWdlLWVuY3J5cHRpb24="
	if got != want {
		t.Errorf("buildOllamaCookieHeader(full-width ;) = %q, want %q", got, want)
	}
	if n := strings.Count(got, ";"); n != 1 {
		t.Errorf("expected exactly one ASCII ';' separator, got %d in %q", n, got)
	}
}

func TestBuildOllamaCookieHeaderCleansPairList(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
		{"bare session value without padding", "abc123", "__Secure-session=abc123"},
		{"bare base64 session value with padding", "YWdlLWVuY3J5cHRpb24ub3JnL3YxCi0-t-BjriN6p-GBLA==", "__Secure-session=YWdlLWVuY3J5cHRpb24ub3JnL3YxCi0-t-BjriN6p-GBLA=="},
		{"named session cookie only", "__Secure-session=YWdlLQ==", "__Secure-session=YWdlLQ=="},
		{"stray separators", "  aid=x ;;  __Secure-session=y ; ", "aid=x; __Secure-session=y"},
		{"full-width with prefix", "Cookie: aid=x；__Secure-session=y", "aid=x; __Secure-session=y"},
		{"repeated full-width", "aid=x；；__Secure-session=y", "aid=x; __Secure-session=y"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildOllamaCookieHeader(tc.in); got != tc.want {
				t.Errorf("buildOllamaCookieHeader(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The 2026-10 re-skin of ollama.com/settings: "Cloud usage" became an <h2>,
// the "Session"/"Weekly" headers gained the " usage" suffix, and the block is
// now terminated by the "notify me" form. The plan badge also moved into the
// "Usage credits" card *above* the heading, so it must come from the whole
// document. This fixture is a real captured page (credentials stripped).
func TestParseOllamaQuotaHTMLNewSettingsLayout(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "ollama_settings_cloud_usage.html"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	now := time.Date(2026, 10, 8, 5, 0, 0, 0, time.UTC)

	plan, windows, err := parseOllamaQuotaHTML(string(raw), now)
	if err != nil {
		t.Fatalf("parseOllamaQuotaHTML(new layout) = %v", err)
	}
	if plan != "pro" {
		t.Errorf("plan = %q, want %q (the badge sits above the Cloud usage heading)", plan, "pro")
	}
	if len(windows) != 2 {
		t.Fatalf("len(windows) = %d, want 2", len(windows))
	}
	if windows[0].Label != "Session" || windows[1].Label != "Weekly" {
		t.Errorf("labels = %q/%q, want Session/Weekly", windows[0].Label, windows[1].Label)
	}
	if windows[0].Used != 0.9 || windows[1].Used != 10.6 {
		t.Errorf("used = %v/%v, want 0.9/10.6", windows[0].Used, windows[1].Used)
	}
	if windows[0].Remaining != 99.1 {
		t.Errorf("session remaining = %v, want 99.1", windows[0].Remaining)
	}
	if windows[0].ResetAt != "2026-10-08T09:00:00Z" || windows[1].ResetAt != "2026-10-12T00:00:00Z" {
		t.Errorf("resets = %q/%q, want 2026-10-08T09:00:00Z/2026-10-12T00:00:00Z",
			windows[0].ResetAt, windows[1].ResetAt)
	}
	if windows[0].ResetInSec != 4*3600 {
		t.Errorf("session resetInSec = %d, want %d", windows[0].ResetInSec, 4*3600)
	}
	for i, wantReqs := range []int64{5, 841} {
		if len(windows[i].Models) != 1 {
			t.Fatalf("windows[%d].Models = %+v, want exactly one entry", i, windows[i].Models)
		}
		if got := windows[i].Models[0]; got.Model != "deepseek-v4.1-flash" || got.Requests != wantReqs {
			t.Errorf("windows[%d].Models[0] = %+v, want deepseek-v4.1-flash/%d", i, got, wantReqs)
		}
	}
}

func TestExtractOllamaCloudUsageBlockAcceptsBothHeadingTags(t *testing.T) {
	meters := `<div class="flex justify-between mb-2">` +
		`<span class="text-sm ">Session usage</span><span class="text-sm ">1% used</span></div>` +
		`<div data-usage-track aria-label="Session usage 1% used"></div>`
	form := `<form method="POST" action="/settings" class="pt-2" hx-post="/settings" hx-swap="none">` +
		`<label>notify</label></form><div>after the usage section</div>`

	for _, heading := range []string{
		`<span>Cloud usage</span>`,
		`<h2 class="text-xl font-medium pt-4">Cloud usage</h2>`,
	} {
		block, err := extractOllamaCloudUsageBlock(heading + meters + form)
		if err != nil {
			t.Fatalf("heading %q: extractOllamaCloudUsageBlock = %v", heading, err)
		}
		if !strings.Contains(block, `aria-label="Session usage 1% used"`) {
			t.Errorf("heading %q: block is missing the usage meter: %q", heading, block)
		}
		if strings.Contains(block, "<form") || strings.Contains(block, "after the usage section") {
			t.Errorf("heading %q: block leaked past the terminating form: %q", heading, block)
		}
	}

	if _, err := extractOllamaCloudUsageBlock(`<html><body>no usage section here</body></html>`); err == nil {
		t.Error("expected an error when the Cloud usage heading is absent")
	}
}

// 2026-11 incident (live ollama.com): the cookie had been relayed through a
// Markdown-ish surface, so the stored value was
//
//	aid=<uuid>,\_\_Secure-session=<base64>
//
// — ";" became "," and "_" was escaped. looksLikeOllamaCookiePairs saw a valid
// short name ("aid") and shipped the whole thing as ONE aid cookie, so the
// session cookie never reached ollama.com and /settings redirected to /signin.
// The dashboard then reported "未登录或 cookie 无效" for a perfectly valid login.
func TestBuildOllamaCookieHeaderRepairsCommaSeparatorAndMarkdownEscape(t *testing.T) {
	const token = "YWdlLWVuY3J5cHRpb24ub3JnL3YxCi0tLS0t"
	in := "aid=46ecb933-35bc-4c97-a651-090bfd36ea89,\\_\\_Secure-session=" + token
	want := "aid=46ecb933-35bc-4c97-a651-090bfd36ea89; __Secure-session=" + token
	got := buildOllamaCookieHeader(in)
	if got != want {
		t.Fatalf("buildOllamaCookieHeader(%q) = %q, want %q", in, got, want)
	}
	if !ollamaCookieHasSession(got) {
		t.Errorf("repaired cookie should carry a session field, got %q", got)
	}
	if n := strings.Count(got, ";"); n != 1 {
		t.Errorf("expected exactly one separator, got %d in %q", n, got)
	}
}

// A comma that is not followed by a known cookie name belongs to the value and
// must survive: base64url/JWT payloads can contain anything after a comma.
func TestBuildOllamaCookieHeaderKeepsCommasInsideValues(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			"aid=x; __Secure-session=abc,def",
			"aid=x; __Secure-session=abc,def",
		},
		{
			"__Secure-session=eyJhIjoxLCJiIjoyfQ",
			"__Secure-session=eyJhIjoxLCJiIjoyfQ",
		},
	}
	for _, tc := range cases {
		if got := buildOllamaCookieHeader(tc.in); got != tc.want {
			t.Errorf("buildOllamaCookieHeader(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// aid is an anonymous visitor id, always present on ollama.com. A cookie that
// carries only aid can never authenticate, so it must be rejected up front with
// an actionable message instead of a generic "cookie 无效".
func TestOllamaCookieHasSession(t *testing.T) {
	aidOnly := buildOllamaCookieHeader("aid=46ecb933-35bc-4c97-a651-090bfd36ea89")
	if ollamaCookieHasSession(aidOnly) {
		t.Errorf("aid-only cookie must not be treated as a session: %q", aidOnly)
	}
	if got := ollamaCookieNameList(aidOnly); got != "aid" {
		t.Errorf("ollamaCookieNameList(%q) = %q, want aid", aidOnly, got)
	}
	for _, in := range []string{
		"aid=x; __Secure-session=t",
		"Cookie: __Secure-session=t",
		"__Secure-session=t",
		"__Host-session=t",
	} {
		if header := buildOllamaCookieHeader(in); !ollamaCookieHasSession(header) {
			t.Errorf("expected a session cookie in %q → %q", in, header)
		}
	}
	if ollamaCookieHasSession("aid=x; __Secure-session=") {
		t.Error("an empty session value must not count as a session")
	}
}

// Regression guard for the live page: the failure surfaced only after following
// ollama.com's 303 → /signin chain. fetchOllamaQuota now stops at the first
// redirect into the login flow, so this test pins the redirect classification.
func TestFetchOllamaQuotaStopsAtSignInRedirect(t *testing.T) {
	if !ollamaRedirectsToSignIn("https://ollama.com/signin") {
		t.Error("https://ollama.com/signin should classify as a sign-in redirect")
	}
	if !ollamaRedirectsToSignIn("https://signin.ollama.com/?client_id=x") {
		t.Error("signin.ollama.com should classify as a sign-in redirect")
	}
	if ollamaRedirectsToSignIn("https://ollama.com/settings") {
		t.Error("the settings page must not classify as a sign-in redirect")
	}
}
