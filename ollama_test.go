package main

import (
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
