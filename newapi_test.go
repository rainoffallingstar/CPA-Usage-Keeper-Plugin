package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNormalizeNewAPIBaseURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "https://dddai.dev"},
		{"dddai.dev", "https://dddai.dev"},
		{"https://dddai.dev/", "https://dddai.dev"},
		{"https://dddai.dev/api", "https://dddai.dev"},
		{"https://dddai.dev/api/", "https://dddai.dev"},
		{"https://dddai.dev/v1", "https://dddai.dev"},
		{"http://localhost:3000/api", "http://localhost:3000"},
		{"https://example.com/panel", "https://example.com/panel"},
	}
	for _, c := range cases {
		if got := normalizeNewAPIBaseURL(c.in); got != c.want {
			t.Errorf("normalizeNewAPIBaseURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// newAPIStockServer emulates a stock QuantumNous/new-api deployment. Only the
// documented self-service routes exist; /api/billing/summary is deliberately
// absent so a regression back to the dddai extension fails loudly.
func newAPIStockServer(t *testing.T, calls *int32) *httptest.Server {
	t.Helper()
	// Relative timestamps: absolute ones silently change the computed day count
	// as the calendar moves (this test broke one day after it was written).
	soon := time.Now().Add(3 * 24 * time.Hour).Unix()
	later := time.Now().Add(11 * 24 * time.Hour).Unix()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/billing/summary", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("provider must not call dddai-only /api/billing/summary")
		http.NotFound(w, r)
	})
	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		w.Write([]byte(`{"success":true,"data":{"quota_per_unit":500000,"quota_display_type":"USD","usd_exchange_rate":7}}`))
	})
	mux.HandleFunc("/api/user/self", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		w.Write([]byte(`{"success":true,"data":{"id":1,"group":"subscriber","quota":302158,"used_quota":568293819}}`))
	})
	mux.HandleFunc("/api/subscription/self", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		w.Write([]byte(fmt.Sprintf(`{"success":true,"data":{"billing_preference":"subscription_first","subscriptions":[
			{"subscription":{"id":1249,"plan_id":19,"status":"active","amount_total":475000000,
			"amount_used":117983493,"end_time":%d}},
			{"subscription":{"id":1250,"plan_id":20,"status":"active","amount_total":360000000,
			"amount_used":100000000,"end_time":%d}},
			{"subscription":{"id":1200,"plan_id":19,"status":"expired","amount_total":1,
			"amount_used":1,"end_time":1}}]}}`, later, soon)))
	})
	mux.HandleFunc("/api/subscription/plans", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		w.Write([]byte(`{"success":true,"data":[
			{"plan":{"id":19,"title":"增强月卡"}},
			{"plan":{"id":20,"title":"Claude Kiro 周卡"}}]}`))
	})
	mux.HandleFunc("/api/log/self/stat", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(calls, 1)
		if r.URL.Query().Get("start_timestamp") == "" || r.URL.Query().Get("end_timestamp") == "" {
			t.Errorf("log/self/stat called without a time range")
		}
		w.Write([]byte(`{"success":true,"data":{"quota":41545743}}`))
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

func TestFetchNewAPISnapshotUsesOnlyStockRoutes(t *testing.T) {
	var calls int32
	srv := newAPIStockServer(t, &calls)
	defer srv.Close()

	snap, warnings, err := fetchNewAPISnapshot(srv.URL, "nap_x")
	if err != nil {
		t.Fatalf("snapshot failed: %v (warnings=%v)", err, warnings)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	assertClose(t, "quota unit", snap.QuotaPerUnit, 500000)
	assertClose(t, "wallet remaining", snap.Wallet.Remaining, 302158)
	assertClose(t, "wallet used", snap.Wallet.Used, 568293819)

	// Expired subscriptions are dropped; active ones stay separate.
	if len(snap.Subscriptions) != 2 {
		t.Fatalf("subscriptions = %d, want 2 active", len(snap.Subscriptions))
	}
	// Sorted by soonest expiry: id 1250 (ends in 3 days) first.
	if snap.Subscriptions[0].ID != 1250 || snap.Subscriptions[1].ID != 1249 {
		t.Errorf("order = %d,%d", snap.Subscriptions[0].ID, snap.Subscriptions[1].ID)
	}
	if snap.Subscriptions[1].PlanTitle != "增强月卡" {
		t.Errorf("plan title = %q, want resolved title", snap.Subscriptions[1].PlanTitle)
	}
	if snap.Subscriptions[1].AmountRemaining != 475000000-117983493 {
		t.Errorf("remaining = %v", snap.Subscriptions[1].AmountRemaining)
	}
	if snap.Subscriptions[1].DaysUntilExpiry != 11 {
		t.Errorf("days = %d, want 11", snap.Subscriptions[1].DaysUntilExpiry)
	}
	if !snap.Estimate.HasRecentUsage || snap.Estimate.WindowDays != 7 {
		t.Errorf("estimate = %+v", snap.Estimate)
	}
	assertClose(t, "avg daily", snap.Estimate.AverageDailyUsage, 41545743.0/7)

	wantRemaining := 302158.0 + (475000000.0 - 117983493) + (360000000.0 - 100000000)
	assertClose(t, "pooled remaining", snap.Estimate.EstimatedAvailableDays*41545743.0/7, wantRemaining)
}

func TestBuildNewAPIQuotaAccountRendersEachSubscription(t *testing.T) {
	var calls int32
	srv := newAPIStockServer(t, &calls)
	defer srv.Close()

	snap, _, err := fetchNewAPISnapshot(srv.URL, "nap_x")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	acct := buildNewAPIQuotaAccount("multi", snap)

	if acct.Plan != "Claude Kiro 周卡 + 增强月卡" {
		t.Errorf("plan = %q, want both active plans", acct.Plan)
	}
	// 2 subscriptions + wallet + estimate.
	if len(acct.Windows) != 4 {
		t.Fatalf("windows = %d, want 4: %+v", len(acct.Windows), acct.Windows)
	}

	first := acct.Windows[0]
	if first.Label != "Claude Kiro 周卡" {
		t.Errorf("first label = %q", first.Label)
	}
	assertClose(t, "first total", first.Total, 720)
	assertClose(t, "first used", first.Used, 200)
	assertClose(t, "first remaining", first.Remaining, 520)

	second := acct.Windows[1]
	if second.Label != "增强月卡" {
		t.Errorf("second label = %q", second.Label)
	}
	assertClose(t, "second total", second.Total, 950)
	assertClose(t, "second remaining", second.Remaining, 714.033014)

	wallet := acct.Windows[2]
	if wallet.Label != "钱包余额" {
		t.Errorf("wallet label = %q", wallet.Label)
	}
	est := acct.Windows[3]
	if est.Label != "用量预估" {
		t.Errorf("estimate label = %q", est.Label)
	}
	// Pooled wording is a hint only; it must warn that quotas are not shared.
	if !strings.Contains(est.StatusText, "各订阅额度不互通") {
		t.Errorf("estimate status = %q, want multi-subscription caveat", est.StatusText)
	}
	if !strings.Contains(est.StatusText, "合计剩余 $1234.64") {
		t.Errorf("estimate status = %q, want pooled total", est.StatusText)
	}
}

func TestBuildNewAPIQuotaAccountDisambiguatesDuplicatePlanTitles(t *testing.T) {
	snap := &newAPISnapshot{
		QuotaPerUnit: 500000,
		Subscriptions: []newAPIUserSubscription{
			{ID: 1, PlanTitle: "增强月卡", UpgradeGroup: "sub-openai", Status: "active", AmountTotal: 100, AmountUsed: 1},
			{ID: 2, PlanTitle: "增强月卡", UpgradeGroup: "sub-claude", Status: "active", AmountTotal: 100, AmountUsed: 1},
		},
	}
	acct := buildNewAPIQuotaAccount("dup", snap)
	if acct.Windows[0].Label != "增强月卡 · sub-openai" || acct.Windows[1].Label != "增强月卡 · sub-claude" {
		t.Errorf("labels = %q, %q", acct.Windows[0].Label, acct.Windows[1].Label)
	}
}

func TestBuildNewAPIQuotaAccountSingleAndUnlimited(t *testing.T) {
	snap := &newAPISnapshot{
		QuotaPerUnit: 500000,
		Panel:        newAPIPanelInfo{QuotaPerUnit: 500000, DisplayType: "USD"},
		Wallet:       newAPIWallet{Remaining: 1000000, Used: 2000000},
		Subscriptions: []newAPIUserSubscription{
			{ID: 1, AmountTotal: 0, Status: "active"}, // total_amount == 0 → unlimited
		},
		Estimate: newAPIBurnEstimate{WindowDays: 7, AverageDailyUsage: 500000, HasRecentUsage: true},
	}
	acct := buildNewAPIQuotaAccount("unlimited", snap)
	if len(acct.Windows) != 3 {
		t.Fatalf("windows = %d, want 3", len(acct.Windows))
	}
	if !strings.Contains(acct.Windows[0].StatusText, "不限量") {
		t.Errorf("status = %q", acct.Windows[0].StatusText)
	}
	// Unlimited makes the day estimate meaningless; it must not claim a number.
	if strings.Contains(acct.Windows[2].StatusText, "≈") || !strings.Contains(acct.Windows[2].StatusText, "含不限量订阅") {
		t.Errorf("estimate status = %q", acct.Windows[2].StatusText)
	}
}

func TestBuildNewAPIQuotaAccountShowsPanelCurrency(t *testing.T) {
	snap := &newAPISnapshot{
		QuotaPerUnit: 500000,
		Panel: newAPIPanelInfo{
			QuotaPerUnit: 500000, DisplayType: "CNY", USDExchangeRate: 6.8,
		},
		Subscriptions: []newAPIUserSubscription{{
			ID: 1, PlanTitle: "增强月卡", Status: "active",
			AmountTotal: 475000000, AmountUsed: 118000000, AmountRemaining: 357000000,
			DaysUntilExpiry: 11,
		}},
	}
	acct := buildNewAPIQuotaAccount("acct", snap)
	// 357000000 / 500000 * 6.8 = 4855.2
	if !strings.Contains(acct.Windows[0].StatusText, "面板显示 ¥4855.20") {
		t.Errorf("status = %q, want panel CNY equivalent", acct.Windows[0].StatusText)
	}
}

func TestFetchNewAPISnapshotPartialFailureKeepsWorkingWindows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/status":
			w.Write([]byte(`{"success":true,"data":{"quota_per_unit":500000}}`))
		case "/api/user/self":
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"success":false,"code":"ACCESS_TOKEN_SCOPE_DENIED","message":"The access token lacks the required permission: profile:read"}`))
		case "/api/subscription/self":
			w.Write([]byte(fmt.Sprintf(`{"success":true,"data":{"subscriptions":[{"subscription":{"id":1,"plan_id":19,"status":"active","amount_total":100000000,"amount_used":25000000,"end_time":%d}}]}}`, time.Now().Add(11*24*time.Hour).Unix())))
		case "/api/subscription/plans":
			w.Write([]byte(`{"success":true,"data":[{"plan":{"id":19,"title":"增强月卡"}}]}`))
		case "/api/log/self/stat":
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"success":false,"message":"The access token lacks the required permission: usage:read"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	snap, warnings, err := fetchNewAPISnapshot(srv.URL, "nap_x")
	if err != nil {
		t.Fatalf("partial failure should still succeed: %v", err)
	}
	if len(warnings) != 2 {
		t.Errorf("warnings = %v, want wallet + usage notes", warnings)
	}
	if !strings.Contains(strings.Join(warnings, " "), "profile:read") ||
		!strings.Contains(strings.Join(warnings, " "), "usage:read") {
		t.Errorf("warnings should carry the panel's scope messages: %v", warnings)
	}
	if len(snap.Subscriptions) != 1 || snap.Subscriptions[0].PlanTitle != "增强月卡" {
		t.Errorf("subscription should still load: %+v", snap.Subscriptions)
	}
	if snap.Wallet.Remaining != 0 {
		t.Errorf("wallet should stay empty on failure: %+v", snap.Wallet)
	}
}

func TestFetchNewAPISnapshotAllSourcesFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"success":false,"code":"ACCESS_TOKEN_SCOPE_DENIED","message":"The access token lacks the required permission: profile:read"}`))
	}))
	defer srv.Close()

	_, _, err := fetchNewAPISnapshot(srv.URL, "nap_bad")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "profile:read") {
		t.Fatalf("error should surface the panel message: %v", err)
	}
}

func TestHandleNewAPIQuotaGetReadsTokenFromHeaders(t *testing.T) {
	newAPIAcctMu.Lock()
	orig := newAPIAccounts
	newAPIAccounts = nil
	newAPIAcctMu.Unlock()
	origRefresh := newAPIRefresh
	newAPIRefresh = func(*newAPIAccountRuntime) {}
	defer func() {
		newAPIRefresh = origRefresh
		newAPIAcctMu.Lock()
		newAPIAccounts = orig
		newAPIAcctMu.Unlock()
	}()

	// The token travels in headers, never in the query string (CPA logs URIs).
	resp := handleNewAPIQuotaGet(
		map[string][]string{"action": {"addaccount"}, "account": {"hdr-acct"}},
		map[string][]string{"X-NewAPI-Token": {"nap_from_header"}, "X-NewAPI-Base-URL": {"panel.example.com/api"}},
	)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, resp.Body)
	}
	acct := findNewAPIAccount("hdr-acct")
	if acct == nil || acct.Token != "nap_from_header" || acct.BaseURL != "https://panel.example.com" {
		t.Fatalf("account = %+v", acct)
	}

	// Lower-case header keys must work too.
	applyNewAPIAccountAction("removeaccount", "hdr-acct", "", "")
	resp = handleNewAPIQuotaGet(
		map[string][]string{"action": {"addaccount"}, "account": {"hdr-lower"}},
		map[string][]string{"x-newapi-token": {"nap_lower"}},
	)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("lower-case headers status = %d", resp.StatusCode)
	}
	if a := findNewAPIAccount("hdr-lower"); a == nil || a.Token != "nap_lower" {
		t.Fatalf("lower-case header not honored: %+v", a)
	}
}

func TestNewAPIAccountAddRemoveLifecycle(t *testing.T) {
	newAPIAcctMu.Lock()
	orig := newAPIAccounts
	newAPIAccounts = nil
	newAPIAcctMu.Unlock()
	origRefresh := newAPIRefresh
	newAPIRefresh = func(*newAPIAccountRuntime) {}
	defer func() {
		newAPIRefresh = origRefresh
		newAPIAcctMu.Lock()
		newAPIAccounts = orig
		newAPIAcctMu.Unlock()
	}()

	resp := applyNewAPIAccountAction("addaccount", "test-newapi", "nap_x", "dddai.dev/api")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add status = %d: %s", resp.StatusCode, resp.Body)
	}
	acct := findNewAPIAccount("test-newapi")
	if acct == nil {
		t.Fatal("account not registered")
	}
	if acct.BaseURL != "https://dddai.dev" {
		t.Errorf("base url = %q", acct.BaseURL)
	}
	if acct.Cache.Type != "newapi" || acct.Cache.Success {
		t.Errorf("cache = %+v", acct.Cache)
	}

	if dup := applyNewAPIAccountAction("addaccount", "test-newapi", "nap_y", ""); dup.StatusCode != http.StatusConflict {
		t.Errorf("duplicate status = %d, want 409", dup.StatusCode)
	}

	// A base-URL-only edit keeps the stored token.
	if up := applyNewAPIAccountAction("settoken", "test-newapi", "", "https://panel.example.com/api"); up.StatusCode != http.StatusOK {
		t.Errorf("base-only update status = %d", up.StatusCode)
	}
	if acct.BaseURL != "https://panel.example.com" || acct.Token != "nap_x" {
		t.Errorf("after base-only update: %q %q", acct.BaseURL, acct.Token)
	}
	// Neither field set is rejected.
	if bad := applyNewAPIAccountAction("settoken", "test-newapi", "", ""); bad.StatusCode != http.StatusBadRequest {
		t.Errorf("empty update status = %d, want 400", bad.StatusCode)
	}

	rem := applyNewAPIAccountAction("removeaccount", "test-newapi", "", "")
	if rem.StatusCode != http.StatusOK || findNewAPIAccount("test-newapi") != nil {
		t.Errorf("remove failed: status=%d", rem.StatusCode)
	}
}

func assertClose(t *testing.T, label string, got, want float64) {
	t.Helper()
	if diff := got - want; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("%s = %.6f, want %.6f", label, got, want)
	}
}

// newAPIKeysServer serves GET /api/token/ with realistic upstream payloads:
// the "key" field is present and both masked and full forms are included to
// prove the plugin drops it at the decoding stage.
func newAPIKeysServer(t *testing.T, total int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/status":
			w.Write([]byte(`{"success":true,"data":{"quota_per_unit":500000}}`))
		case "/api/token/":
			page := parseIntVal(r.URL.Query().Get("p"))
			size := parseIntVal(r.URL.Query().Get("page_size"))
			if page < 1 {
				page = 1
			}
			if size <= 0 {
				size = 100
			}
			start := (page - 1) * size
			items := make([]map[string]any, 0, size)
			for i := start; i < total && i < start+size; i++ {
				items = append(items, map[string]any{
					"id":              i + 1,
					"name":            "key-" + string(rune('a'+i%26)),
					"key":             "sk-SECRETPLAINTEXT" + string(rune('0'+i%10)),
					"status":          1,
					"group":           "default",
					"used_quota":      2500000,
					"remain_quota":    7500000,
					"unlimited_quota": false,
					"created_time":    1790000000,
					"accessed_time":   1792380000,
					"expired_time":    -1,
				})
			}
			w.Write(mustJSON(map[string]any{
				"success": true,
				"data":    map[string]any{"page": page, "page_size": size, "total": total, "items": items},
			}))
		default:
			http.NotFound(w, r)
		}
	}))
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func TestFetchNewAPITokensPaginatesAndNeverKeepsPlaintext(t *testing.T) {
	srv := newAPIKeysServer(t, 250)
	defer srv.Close()

	items, err := fetchNewAPITokens(srv.URL, "nap_x")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(items) != 250 {
		t.Fatalf("items = %d, want 250 (3 pages)", len(items))
	}
	if items[0].Name == "" || items[0].ID != 1 {
		t.Errorf("first item = %+v", items[0])
	}

	// The struct has no Key field, so even the upstream payload must not be
	// able to smuggle a secret into memory or a later JSON response.
	raw, _ := json.Marshal(items)
	if strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "sk-") {
		t.Fatalf("plaintext key leaked into decoded struct: %s", raw)
	}
	for _, it := range items {
		if strings.Contains(it.Name, "sk-") {
			t.Fatalf("name carries a secret: %q", it.Name)
		}
	}
}

func TestBuildNewAPIKeysNeverExposesSecret(t *testing.T) {
	items := []newAPITokenItem{
		{ID: 1, Name: "prod", Status: 1, UsedQuota: 2500000, RemainQuota: 7500000},
		{ID: 2, Name: "unlimited", Status: 1, UnlimitedQuota: true, UsedQuota: 1000000, RemainQuota: -1000000},
		{ID: 3, Name: "expired", Status: 3, ExpiredTime: 100},
		{ID: 4, Name: "disabled", Status: 2, UsedQuota: 0},
	}
	snap := &newAPISnapshot{
		QuotaPerUnit: 500000,
		Panel:        newAPIPanelInfo{QuotaPerUnit: 500000, DisplayType: "USD"},
	}
	keys := buildNewAPIKeys(items, snap)
	byID := make(map[int64]newAPIKey, len(keys))
	for _, k := range keys {
		byID[k.ID] = k
	}

	prod := byID[1]
	if prod.UsedUSD != 5 || prod.RemainingUSD != 15 || prod.TotalUSD != 20 {
		t.Errorf("normal key = %+v", prod)
	}
	if prod.Status != "启用" || prod.StatusText == "" {
		t.Errorf("normal status = %q / %q", prod.Status, prod.StatusText)
	}
	if prod.Window == nil || prod.Window.Total != 20 {
		t.Errorf("window = %+v", prod.Window)
	}

	unlimited := byID[2]
	if !unlimited.Unlimited || unlimited.StatusText != "不限量" || unlimited.Window.Total != 0 {
		t.Errorf("unlimited = %+v / %+v", unlimited, unlimited.Window)
	}
	// Upstream stores a negative remain_quota for unlimited tokens; it must not
	// surface as a negative balance in the UI.
	if unlimited.RemainingUSD != 0 || unlimited.Window.Remaining != 0 {
		t.Errorf("unlimited remaining = %v / %v, want 0", unlimited.RemainingUSD, unlimited.Window.Remaining)
	}
	if byID[3].Status != "已过期" {
		t.Errorf("expired status = %q", byID[3].Status)
	}
	if byID[4].Status != "已禁用" {
		t.Errorf("disabled status = %q", byID[4].Status)
	}

	raw, _ := json.Marshal(keys)
	for _, forbidden := range []string{"secret", "sk-", "\"key\""} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Fatalf("response exposes %q: %s", forbidden, raw)
		}
	}
}

func TestBuildNewAPIKeysSortsByUsageAndClampsNegativeRemain(t *testing.T) {
	items := []newAPITokenItem{
		{ID: 1, Name: "small", Status: 1, UsedQuota: 100000, RemainQuota: 900000},
		{ID: 2, Name: "big", Status: 1, UsedQuota: 9000000, RemainQuota: 1000000},
		{ID: 3, Name: "odd", Status: 1, UsedQuota: 500000, RemainQuota: -500000},
	}
	snap := &newAPISnapshot{QuotaPerUnit: 500000, Panel: newAPIPanelInfo{QuotaPerUnit: 500000}}
	keys := buildNewAPIKeys(items, snap)

	if keys[0].Name != "big" || keys[1].Name != "odd" || keys[2].Name != "small" {
		t.Errorf("order = %q,%q,%q", keys[0].Name, keys[1].Name, keys[2].Name)
	}
	if keys[1].RemainingUSD != 0 {
		t.Errorf("negative remain leaked: %v", keys[1].RemainingUSD)
	}
}

func TestHandleNewAPIKeysErrorShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/status" {
			w.Write([]byte(`{"success":true,"data":{"quota_per_unit":500000}}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"success":false,"code":"ACCESS_TOKEN_SCOPE_DENIED","message":"The access token lacks the required permission: api_key:read"}`))
	}))
	defer srv.Close()

	newAPIAcctMu.Lock()
	orig := newAPIAccounts
	newAPIAccounts = []*newAPIAccountRuntime{{
		Name: "k", BaseURL: srv.URL, Token: "nap_x", Cache: newAPIAccountCache("k"),
	}}
	newAPIAcctMu.Unlock()
	defer func() {
		newAPIAcctMu.Lock()
		newAPIAccounts = orig
		newAPIAcctMu.Unlock()
	}()

	resp := handleNewAPIQuotaGet(map[string][]string{"action": {"keys"}, "account": {"k"}}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body struct {
		Keys  []newAPIKey `json:"keys"`
		Error string      `json:"error"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.Contains(body.Error, "api_key:read") {
		t.Errorf("error = %q, want scope hint", body.Error)
	}
}

// TestBuildNewAPIKeysPanelAnnotationMatchesBalance makes sure the CNY
// annotation describes the remaining quota. It used to be panel_used, which is
// lifetime spend and produced figures like ¥692.04 next to a $105.00 balance.
func TestBuildNewAPIKeysPanelAnnotationMatchesBalance(t *testing.T) {
	items := []newAPITokenItem{{
		ID: 1, Name: "team", Status: 1,
		UsedQuota:   50885000, // $101.77 lifetime
		RemainQuota: 52500000, // $105.00 remaining
	}}
	snap := &newAPISnapshot{
		QuotaPerUnit: 500000,
		Panel:        newAPIPanelInfo{QuotaPerUnit: 500000, DisplayType: "CNY", USDExchangeRate: 6.8},
	}
	k := buildNewAPIKeys(items, snap)[0]

	if k.PanelRemaining != "¥714.00" {
		t.Errorf("panel remaining = %q, want ¥714.00 (105 x 6.8)", k.PanelRemaining)
	}
	if k.PanelUsed != "¥692.04" {
		t.Errorf("panel used = %q, want ¥692.04", k.PanelUsed)
	}
}

// TestBuildNewAPIKeysKeepsZeroCycleUsed guards a JSON-encoding trap: with
// omitempty, a freshly reset key (cycle_used_usd == 0) dropped the field
// entirely, so clients reading it saw undefined exactly when the value mattered.
func TestBuildNewAPIKeysKeepsZeroCycleUsed(t *testing.T) {
	d, cleanup := setupTestDB(t)
	defer cleanup()
	installTestDB(t, d)
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS newapi_key_cycles (
		account TEXT NOT NULL, key_id INTEGER NOT NULL,
		baseline_used REAL NOT NULL DEFAULT 0, cycle_total REAL NOT NULL DEFAULT 0,
		reset_at TEXT NOT NULL DEFAULT '', PRIMARY KEY (account, key_id))`); err != nil {
		t.Fatalf("create: %v", err)
	}
	cycles := map[int64]newAPIKeyCycle{1: {
		Account: "acct", KeyID: 1, BaselineUsed: 70000000, CycleTotal: 102500000,
		ResetAt: "2026-10-21T11:45:13Z",
	}}
	// used_quota equals the baseline: nothing consumed since the reset.
	items := []newAPITokenItem{{ID: 1, Name: "team", Status: 1, UsedQuota: 70000000, RemainQuota: 102500000}}
	snap := &newAPISnapshot{QuotaPerUnit: 500000, Panel: newAPIPanelInfo{QuotaPerUnit: 500000}}
	keys := buildNewAPIKeysWithCycles(items, snap, cycles)

	raw, _ := json.Marshal(keys[0])
	if !strings.Contains(string(raw), `"cycle_used_usd":0`) {
		t.Fatalf("zero cycle usage dropped from JSON: %s", raw)
	}
	if !strings.Contains(string(raw), `"cycle_active":true`) {
		t.Fatalf("cycle_active missing: %s", raw)
	}
	if !strings.Contains(string(raw), `"cycle_total_usd":205`) {
		t.Fatalf("cycle total missing: %s", raw)
	}
}
