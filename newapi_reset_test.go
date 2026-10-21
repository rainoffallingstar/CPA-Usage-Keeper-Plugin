package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeTokenPanel emulates the upstream token read/update endpoints, including
// the destructive behaviour that makes a naive reset dangerous: PUT /api/token/
// overwrites every assignable field with whatever the body contains.
type fakeTokenPanel struct {
	mu       sync.Mutex
	detail   newAPITokenDetail
	puts     []newAPITokenQuotaRequest
	statusUp []int
	reject   bool
	units    float64
}

func (f *fakeTokenPanel) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Write(mustJSON(map[string]any{"success": true, "data": map[string]any{"quota_per_unit": f.units}}))
	})
	mux.HandleFunc("/api/token/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("unexpected method %s", r.Method)
			http.NotFound(w, r)
			return
		}
		var body newAPITokenQuotaRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.reject {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"success":false,"code":"ACCESS_TOKEN_SCOPE_DENIED","message":"The access token lacks the required permission: api_key:write"}`))
			return
		}
		f.puts = append(f.puts, body)
		if r.URL.Query().Get("status_only") != "" {
			f.statusUp = append(f.statusUp, body.Status)
			f.detail.Status = body.Status
		} else {
			// Faithful to upstream Token.Update(): every selected column is
			// written from the request, so omissions become zero values.
			f.detail.Name = body.Name
			f.detail.Status = body.Status
			f.detail.ExpiredTime = body.ExpiredTime
			f.detail.RemainQuota = body.RemainQuota
			f.detail.UnlimitedQuota = body.UnlimitedQuota
			f.detail.ModelLimitsEnabled = body.ModelLimitsEnabled
			f.detail.ModelLimits = body.ModelLimits
			f.detail.AllowIPs = body.AllowIPs
			f.detail.Group = body.Group
			f.detail.CrossGroupRetry = body.CrossGroupRetry
		}
		w.Write(mustJSON(map[string]any{"success": true, "message": "", "data": f.detail}))
	})
	mux.HandleFunc("/api/token/9876", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Write(mustJSON(map[string]any{"success": true, "data": f.detail}))
	})
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer nap_x" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"success":false,"message":"invalid token"}`))
			return
		}
		mux.ServeHTTP(w, r)
	}))
}

func newFakePanel() *fakeTokenPanel {
	ips := ""
	return &fakeTokenPanel{
		units: 500000,
		detail: newAPITokenDetail{
			ID: 9876, Name: "team", Status: 1, ExpiredTime: -1,
			RemainQuota: 51336729, UsedQuota: 50712681,
			ModelLimitsEnabled: true,
			ModelLimits:        "gpt-6.1-sol,gpt-6-sol,gpt-5.6-luna",
			AllowIPs:           &ips, Group: "sub-openai",
		},
	}
}

func TestResetNewAPIKeyQuotaPreservesEveryOtherField(t *testing.T) {
	panel := newFakePanel()
	srv := panel.server(t)
	defer srv.Close()

	after, err := resetNewAPIKeyQuota(srv.URL, "nap_x", 9876, 50, 500000)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	// 50 USD × 500000 units/USD
	if after.RemainQuota != 25000000 {
		t.Errorf("remain = %v, want 25000000", after.RemainQuota)
	}
	// The destructive part: nothing else may be blanked.
	if after.Name != "team" || after.Group != "sub-openai" ||
		after.ModelLimits != "gpt-6.1-sol,gpt-6-sol,gpt-5.6-luna" ||
		!after.ModelLimitsEnabled || after.ExpiredTime != -1 {
		t.Fatalf("fields clobbered: %+v", after)
	}
	if after.AllowIPs == nil || *after.AllowIPs != "" {
		t.Errorf("allow_ips = %v, want empty string not null", after.AllowIPs)
	}
	if after.UsedQuota != 50712681 {
		t.Errorf("used_quota changed: %v", after.UsedQuota)
	}

	if len(panel.puts) != 1 {
		t.Fatalf("PUTs = %d, want 1", len(panel.puts))
	}
	body := panel.puts[0]
	if body.ID != 9876 || body.Name != "team" || body.Group != "sub-openai" {
		t.Errorf("request body not a faithful copy: %+v", body)
	}
}

func TestResetNewAPIKeyQuotaRevivesExhaustedKey(t *testing.T) {
	panel := newFakePanel()
	panel.detail.Status = 4 // exhausted: upstream refuses to enable it while <= 0
	panel.detail.RemainQuota = 0
	srv := panel.server(t)
	defer srv.Close()

	after, err := resetNewAPIKeyQuota(srv.URL, "nap_x", 9876, 20, 500000)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if len(panel.statusUp) != 1 || panel.statusUp[0] != 1 {
		t.Errorf("status-only call = %v, want one enable", panel.statusUp)
	}
	if after.Status != 1 || after.RemainQuota != 10000000 {
		t.Errorf("after = %+v", after)
	}
	// Order matters: the balance must be written before the enable attempt.
	if len(panel.puts) != 2 {
		t.Fatalf("PUTs = %d, want quota + status", len(panel.puts))
	}
}

func TestResetNewAPIKeyQuotaRejectsUnlimitedAndBadInput(t *testing.T) {
	panel := newFakePanel()
	panel.detail.UnlimitedQuota = true
	srv := panel.server(t)
	defer srv.Close()

	if _, err := resetNewAPIKeyQuota(srv.URL, "nap_x", 9876, 10, 500000); err == nil ||
		!strings.Contains(err.Error(), "不限量") {
		t.Errorf("unlimited error = %v", err)
	}
	if _, err := resetNewAPIKeyQuota(srv.URL, "nap_x", 9876, 0, 500000); err == nil {
		t.Error("zero amount should be rejected")
	}
}

func TestResetNewAPIKeyQuotaSurfacesScopeDenied(t *testing.T) {
	panel := newFakePanel()
	panel.reject = true
	srv := panel.server(t)
	defer srv.Close()

	_, err := resetNewAPIKeyQuota(srv.URL, "nap_x", 9876, 10, 500000)
	if err == nil || !strings.Contains(err.Error(), "api_key:write") {
		t.Fatalf("error = %v, want panel scope message", err)
	}
	if panel.detail.Name != "team" || panel.detail.RemainQuota != 51336729 {
		t.Errorf("failed write must not mutate state: %+v", panel.detail)
	}
}

func TestHandleNewAPIResetKeyValidatesInput(t *testing.T) {
	newAPIAcctMu.Lock()
	orig := newAPIAccounts
	newAPIAccounts = []*newAPIAccountRuntime{
		{Name: "acct", BaseURL: "https://panel.invalid", Token: "nap_x", Cache: newAPIAccountCache("acct")},
	}
	newAPIAcctMu.Unlock()
	defer func() {
		newAPIAcctMu.Lock()
		newAPIAccounts = orig
		newAPIAcctMu.Unlock()
	}()

	cases := []struct {
		name   string
		keyID  int64
		amount string
		want   int
	}{
		{"missing id", 0, "10", http.StatusBadRequest},
		{"zero amount", 1, "0", http.StatusBadRequest},
		{"negative amount", 1, "-5", http.StatusBadRequest},
		{"non-numeric", 1, "abc", http.StatusBadRequest},
	}
	for _, c := range cases {
		// Validation must happen before any network call, so an unreachable
		// panel still yields 400 rather than 502.
		resp := handleNewAPIResetKey("acct", c.keyID, c.amount)
		if resp.StatusCode != c.want {
			t.Errorf("%s: status = %d, want %d", c.name, resp.StatusCode, c.want)
		}
	}
	if resp := handleNewAPIResetKey("nope", 1, "10"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown account status = %d", resp.StatusCode)
	}
}
