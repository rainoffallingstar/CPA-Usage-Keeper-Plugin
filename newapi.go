package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// ---------------------------------------------------------------------------
// NewAPI-compatible panel quota monitoring.
//
// Credential: the panel *access token* ("nap_..."), not a model API key
// ("sk-..."). It is sent as "Authorization: Bearer <token>".
//
// Only routes that stock QuantumNous/new-api exposes are used, so the provider
// works against any NewAPI deployment (and does not depend on dddai-specific
// extensions such as /api/billing/summary):
//
//	GET /api/status              public; quota_per_unit, display currency
//	GET /api/user/self           wallet (needs profile:read)
//	GET /api/subscription/self   active subscriptions (needs wallet:read)
//	GET /api/subscription/plans  plan_id -> title (needs wallet:read)
//	GET /api/log/self/stat       consumed quota in a range (needs usage:read)
//
// A user may hold several active subscriptions at once (e.g. sub-openai and
// sub-claude-kiro). Their quotas are scoped to different groups and must not be
// summed, so each active subscription is rendered as its own window.
// ---------------------------------------------------------------------------

const (
	newAPIDefaultBaseURL   = "https://dddai.dev"
	newAPITimeout          = 20 * time.Second
	newAPIDefaultQuotaUnit = 500000.0
	newAPIEstimateDays     = 7
)

// Upstream data shapes -------------------------------------------------------

type newAPIStatusData struct {
	Version                    string  `json:"version"`
	SystemName                 string  `json:"system_name"`
	QuotaPerUnit               float64 `json:"quota_per_unit"`
	DisplayInCurrency          bool    `json:"display_in_currency"`
	QuotaDisplayType           string  `json:"quota_display_type"`
	CustomCurrencySymbol       string  `json:"custom_currency_symbol"`
	CustomCurrencyExchangeRate float64 `json:"custom_currency_exchange_rate"`
	USDExchangeRate            float64 `json:"usd_exchange_rate"`
}

type newAPIUserSelfData struct {
	ID           int64   `json:"id"`
	Username     string  `json:"username"`
	DisplayName  string  `json:"display_name"`
	Email        string  `json:"email"`
	Group        string  `json:"group"`
	Quota        float64 `json:"quota"`
	UsedQuota    float64 `json:"used_quota"`
	RequestCount float64 `json:"request_count"`
}

type newAPIUserSubscription struct {
	ID              int64   `json:"id"`
	PlanID          int64   `json:"plan_id"`
	PlanTitle       string  `json:"plan_title"`
	Status          string  `json:"status"`
	StartTime       int64   `json:"start_time"`
	EndTime         int64   `json:"end_time"`
	NextResetTime   int64   `json:"next_reset_time"`
	QuotaReset      string  `json:"quota_reset_period"`
	UpgradeGroup    string  `json:"upgrade_group"`
	AmountTotal     float64 `json:"amount_total"`
	AmountUsed      float64 `json:"amount_used"`
	AmountRemaining float64 `json:"amount_remaining"`
	Unlimited       bool    `json:"unlimited"`
	DaysUntilExpiry int     `json:"days_until_expiry"`
}

type newAPIBurnEstimate struct {
	WindowDays             int
	RecentUsage            float64
	AverageDailyUsage      float64
	EstimatedAvailableDays float64
	HasRecentUsage         bool
}

// newAPIPanelInfo carries the panel's display settings from /api/status.
type newAPIPanelInfo struct {
	QuotaPerUnit    float64
	DisplayType     string
	USDExchangeRate float64
	CustomSymbol    string
	CustomRate      float64
}

// newAPISnapshot is the normalized panel state assembled from the routes above.
type newAPISnapshot struct {
	BaseURL           string
	BillingPreference string
	Wallet            newAPIWallet
	Subscriptions     []newAPIUserSubscription // active only, earliest expiry first
	Estimate          newAPIBurnEstimate
	QuotaPerUnit      float64
	Panel             newAPIPanelInfo
}

type newAPIWallet struct {
	Remaining float64 `json:"remaining"`
	Used      float64 `json:"used"`
	Total     float64 `json:"total"`
}

type newAPIAccountRuntime struct {
	Name    string
	BaseURL string
	Token   string
	Cache   quotaAccount
	CacheMu sync.RWMutex
}

var (
	newAPIAccounts   []*newAPIAccountRuntime
	newAPIAcctMu     sync.RWMutex
	newAPIHTTPClient = &http.Client{Timeout: newAPITimeout}
)

// newAPIRefresh is swapped out by tests so account lifecycle tests never hit
// the network.
var newAPIRefresh = refreshNewAPIQuota

// normalizeNewAPIBaseURL accepts a bare host, a full URL, or one accidentally
// suffixed with /api or /v1, and returns the panel root (no trailing slash).
func normalizeNewAPIBaseURL(raw string) string {
	base := strings.TrimSpace(raw)
	if base == "" {
		return newAPIDefaultBaseURL
	}
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "https://" + base
	}
	base = strings.TrimRight(base, "/")
	for _, suffix := range []string{"/api", "/v1"} {
		if len(base) > len(suffix) && strings.HasSuffix(strings.ToLower(base), suffix) {
			base = strings.TrimRight(base[:len(base)-len(suffix)], "/")
		}
	}
	return base
}

// newAPIGetJSON performs a GET and decodes a JSON body. A non-empty token is
// sent as a Bearer credential; /api/status is public and passes "".
func newAPIGetJSON(url, token string, out any) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := newAPIHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, apiErrorMessage(body, resp.StatusCode))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("invalid JSON response")
	}
	return nil
}

// apiErrorMessage extracts the panel's own error text so scope/token problems
// surface verbatim ("The access token lacks the required permission: …").
func apiErrorMessage(body []byte, status int) string {
	var e struct {
		Message string `json:"message"`
		Code    string `json:"code"`
		Error   string `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	for _, candidate := range []string{e.Message, e.Code, e.Error} {
		if s := strings.TrimSpace(candidate); s != "" {
			return s
		}
	}
	msg := strings.TrimSpace(string(body))
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	return msg
}

// fetchNewAPIPanelInfo reads public panel settings. Failure is non-fatal:
// callers fall back to NewAPI's documented defaults.
func fetchNewAPIPanelInfo(base string) newAPIPanelInfo {
	info := newAPIPanelInfo{
		QuotaPerUnit:    newAPIDefaultQuotaUnit,
		DisplayType:     "USD",
		USDExchangeRate: 1,
	}
	var r struct {
		Data newAPIStatusData `json:"data"`
	}
	if err := newAPIGetJSON(base+"/api/status", "", &r); err != nil {
		return info
	}
	d := r.Data
	if d.QuotaPerUnit > 0 {
		info.QuotaPerUnit = d.QuotaPerUnit
	}
	if t := strings.ToUpper(strings.TrimSpace(d.QuotaDisplayType)); t != "" {
		info.DisplayType = t
	}
	if d.USDExchangeRate > 0 {
		info.USDExchangeRate = d.USDExchangeRate
	}
	info.CustomSymbol = strings.TrimSpace(d.CustomCurrencySymbol)
	if d.CustomCurrencyExchangeRate > 0 {
		info.CustomRate = d.CustomCurrencyExchangeRate
	}
	return info
}

// quotaToDisplay renders a quota amount in the panel's own display currency,
// mirroring controller/billing.go: CNY = quota / quota_per_unit × rate.
func (p newAPIPanelInfo) quotaToDisplay(quota, unit float64) (string, bool) {
	if unit <= 0 {
		unit = p.QuotaPerUnit
	}
	if unit <= 0 {
		unit = newAPIDefaultQuotaUnit
	}
	switch p.DisplayType {
	case "CNY":
		rate := p.USDExchangeRate
		if rate <= 0 {
			rate = 1
		}
		return fmt.Sprintf("¥%.2f", quota/unit*rate), true
	case "CUSTOM":
		sym := p.CustomSymbol
		if sym == "" {
			sym = "¤"
		}
		rate := p.CustomRate
		if rate <= 0 {
			rate = 1
		}
		return fmt.Sprintf("%s%.2f", sym, quota/unit*rate), true
	case "TOKENS":
		return fmt.Sprintf("%.0f tokens", quota), true
	}
	return "", false
}

// fetchNewAPIPlanTitles maps plan_id → title via /api/subscription/plans.
// Upstream UserSubscription carries plan_id only, not a title.
func fetchNewAPIPlanTitles(base, token string) (map[int64]string, error) {
	var r struct {
		Data []struct {
			Plan struct {
				ID    int64  `json:"id"`
				Title string `json:"title"`
			} `json:"plan"`
		} `json:"data"`
	}
	if err := newAPIGetJSON(base+"/api/subscription/plans", token, &r); err != nil {
		return nil, err
	}
	titles := make(map[int64]string, len(r.Data))
	for _, item := range r.Data {
		if item.Plan.ID > 0 && strings.TrimSpace(item.Plan.Title) != "" {
			titles[item.Plan.ID] = strings.TrimSpace(item.Plan.Title)
		}
	}
	return titles, nil
}

// fetchNewAPISnapshot assembles the panel state from stock new-api routes.
// Individual route failures are collected in warnings: as long as at least one
// data source responds, the card still renders what is available.
func fetchNewAPISnapshot(baseURL, token string) (*newAPISnapshot, []string, error) {
	base := normalizeNewAPIBaseURL(baseURL)
	panel := fetchNewAPIPanelInfo(base)
	snap := &newAPISnapshot{BaseURL: base, QuotaPerUnit: panel.QuotaPerUnit, Panel: panel}
	var warnings []string
	okCount := 0

	// 1) Wallet.
	var self struct {
		Data newAPIUserSelfData `json:"data"`
	}
	if err := newAPIGetJSON(base+"/api/user/self", token, &self); err != nil {
		warnings = append(warnings, "钱包余额读取失败（需 profile:read）："+err.Error())
	} else {
		snap.Wallet = newAPIWallet{
			Remaining: self.Data.Quota,
			Used:      self.Data.UsedQuota,
			Total:     self.Data.Quota + self.Data.UsedQuota,
		}
		okCount++
	}

	// 2) Subscriptions. A user may hold several active ones at the same time;
	// keep them separate because each is bound to its own group.
	var subs struct {
		Data struct {
			BillingPreference string `json:"billing_preference"`
			Subscriptions     []struct {
				Subscription newAPIUserSubscription `json:"subscription"`
			} `json:"subscriptions"`
		} `json:"data"`
	}
	if err := newAPIGetJSON(base+"/api/subscription/self", token, &subs); err != nil {
		warnings = append(warnings, "订阅额度读取失败（需 wallet:read）："+err.Error())
	} else {
		snap.BillingPreference = subs.Data.BillingPreference
		okCount++

		needTitles := false
		for _, w := range subs.Data.Subscriptions {
			if strings.TrimSpace(w.Subscription.PlanTitle) == "" {
				needTitles = true
				break
			}
		}
		var titles map[int64]string
		if needTitles {
			var terr error
			titles, terr = fetchNewAPIPlanTitles(base, token)
			if terr != nil {
				warnings = append(warnings, "套餐名称读取失败（需 wallet:read）："+terr.Error())
			}
		}

		now := time.Now().Unix()
		for _, w := range subs.Data.Subscriptions {
			s := w.Subscription
			if !strings.EqualFold(strings.TrimSpace(s.Status), "active") {
				continue
			}
			if strings.TrimSpace(s.PlanTitle) == "" && titles != nil {
				s.PlanTitle = titles[s.PlanID]
			}
			if s.AmountRemaining <= 0 && s.AmountTotal > s.AmountUsed {
				s.AmountRemaining = s.AmountTotal - s.AmountUsed
			}
			if s.DaysUntilExpiry <= 0 && s.EndTime > now {
				s.DaysUntilExpiry = int((s.EndTime - now + 86399) / 86400)
			}
			// Upstream models an unlimited plan with total_amount == 0.
			if s.AmountTotal <= 0 {
				s.Unlimited = true
			}
			snap.Subscriptions = append(snap.Subscriptions, s)
		}
		// Soonest expiry first — that is the one users need to act on.
		sort.SliceStable(snap.Subscriptions, func(i, j int) bool {
			return snap.Subscriptions[i].EndTime < snap.Subscriptions[j].EndTime
		})
	}

	if okCount == 0 {
		return nil, warnings, fmt.Errorf("%s", strings.Join(warnings, "；"))
	}

	// 3) Burn rate from /api/log/self/stat over the last 7 days.
	now := time.Now().Unix()
	start := now - int64(newAPIEstimateDays)*24*60*60
	statURL := fmt.Sprintf("%s/api/log/self/stat?start_timestamp=%d&end_timestamp=%d", base, start, now)
	var stat struct {
		Data struct {
			Quota float64 `json:"quota"`
		} `json:"data"`
	}
	if err := newAPIGetJSON(statURL, token, &stat); err != nil {
		warnings = append(warnings, "用量统计读取失败（需 usage:read）："+err.Error())
	} else if stat.Data.Quota > 0 {
		recent := stat.Data.Quota
		est := newAPIBurnEstimate{
			WindowDays:        newAPIEstimateDays,
			RecentUsage:       recent,
			AverageDailyUsage: recent / float64(newAPIEstimateDays),
			HasRecentUsage:    true,
		}
		remaining, hasUnlimited := newAPIRemainingQuota(snap)
		if !hasUnlimited && est.AverageDailyUsage > 0 {
			est.EstimatedAvailableDays = remaining / est.AverageDailyUsage
		}
		snap.Estimate = est
	}

	return snap, warnings, nil
}

// newAPIRemainingQuota sums the wallet and every finite subscription. It also
// reports whether any subscription is unlimited (making the day estimate
// meaningless).
func newAPIRemainingQuota(snap *newAPISnapshot) (float64, bool) {
	remaining := snap.Wallet.Remaining
	hasUnlimited := false
	for _, s := range snap.Subscriptions {
		if s.Unlimited || s.AmountTotal <= 0 {
			hasUnlimited = true
			continue
		}
		remaining += s.AmountRemaining
	}
	return remaining, hasUnlimited
}

// buildNewAPIQuotaAccount projects the snapshot into the shared quota card
// model. Values are converted from internal quota units to USD so the
// dashboard renders real currency instead of raw integers.
func buildNewAPIQuotaAccount(name string, snap *newAPISnapshot) quotaAccount {
	units := snap.QuotaPerUnit
	if units <= 0 {
		units = snap.Panel.QuotaPerUnit
	}
	if units <= 0 {
		// Defensive fallback: some forks omit quota_per_unit and already
		// return USD. Avoid dividing by zero.
		units = 1
	}
	usd := func(v float64) float64 { return v / units }

	acct := quotaAccount{
		Type:      "newapi",
		Name:      name,
		BaseURL:   snap.BaseURL,
		Success:   true,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Windows:   make([]quotaWindow, 0, len(snap.Subscriptions)+2),
	}

	// Plan names. A user can hold several subscriptions at once; each is bound
	// to its own group, so the titles are listed rather than merged.
	var plans []string
	seenPlan := make(map[string]bool, len(snap.Subscriptions))
	for _, s := range snap.Subscriptions {
		title := newAPIPlanLabel(s)
		if title == "" || seenPlan[title] {
			continue
		}
		seenPlan[title] = true
		plans = append(plans, title)
	}
	acct.Plan = strings.Join(plans, " + ")

	// 1) One window per active subscription. Quotas are group-scoped, so they
	// are never summed into a single bar. Labels are plan titles; only
	// genuinely ambiguous duplicates get the group/id appended.
	labelCount := make(map[string]int, len(snap.Subscriptions))
	for _, s := range snap.Subscriptions {
		labelCount[newAPIPlanLabel(s)]++
	}
	for _, s := range snap.Subscriptions {
		label := newAPIPlanLabel(s)
		if labelCount[label] > 1 {
			if group := strings.TrimSpace(s.UpgradeGroup); group != "" {
				label += " · " + group
			} else {
				label += fmt.Sprintf(" · #%d", s.ID)
			}
		}

		expiry := newAPIExpirySuffix(s.DaysUntilExpiry)
		reset := newAPIResetSuffix(s.NextResetTime)
		// Upstream models an unlimited plan with total_amount == 0; treat it
		// defensively here too so callers cannot build a "$0 / $0" bar.
		if s.Unlimited || s.AmountTotal <= 0 {
			acct.Windows = append(acct.Windows, quotaWindow{
				Label:      label,
				Unit:       "USD",
				StatusText: "不限量订阅" + expiry + reset,
				Display:    "text",
			})
			continue
		}

		remaining := s.AmountRemaining
		if remaining <= 0 && s.AmountTotal > s.AmountUsed {
			remaining = s.AmountTotal - s.AmountUsed
		}
		status := fmt.Sprintf("剩余 $%.2f", usd(remaining)) + expiry + reset
		if panelAmount, ok := snap.Panel.quotaToDisplay(remaining, units); ok {
			status += " · 面板显示 " + panelAmount
		}
		acct.Windows = append(acct.Windows, quotaWindow{
			Label:      label,
			Used:       usd(s.AmountUsed),
			Total:      usd(s.AmountTotal),
			Remaining:  usd(remaining),
			Unit:       "USD",
			StatusText: status,
		})
	}

	// 2) Wallet. total/used are lifetime cumulative figures, not a budget, so
	// a used/total bar would read ~100% forever. Show the balance as text.
	if snap.Wallet.Total > 0 || snap.Wallet.Remaining != 0 {
		acct.Windows = append(acct.Windows, quotaWindow{
			Label:      "钱包余额",
			Unit:       "USD",
			Remaining:  usd(snap.Wallet.Remaining),
			StatusText: fmt.Sprintf("累计已用 $%.2f（历史累计）", usd(snap.Wallet.Used)),
		})
	}

	// 3) Burn-rate estimate over the whole account (wallet + all finite
	// subscriptions), with a note that group-scoped quotas cannot actually be
	// pooled.
	if snap.Estimate.HasRecentUsage && snap.Estimate.AverageDailyUsage > 0 {
		status := fmt.Sprintf("近 %d 天日均 $%.2f", snap.Estimate.WindowDays, usd(snap.Estimate.AverageDailyUsage))
		remaining, hasUnlimited := newAPIRemainingQuota(snap)
		switch {
		case hasUnlimited:
			status += " · 含不限量订阅"
		case remaining > 0:
			status += fmt.Sprintf(" · 合计剩余 $%.2f ≈ %.1f 天", usd(remaining), snap.Estimate.EstimatedAvailableDays)
			if len(snap.Subscriptions) > 1 {
				status += "（各订阅额度不互通）"
			}
		}
		acct.Windows = append(acct.Windows, quotaWindow{
			Label:      "用量预估",
			Unit:       "USD/天",
			StatusText: status,
			Display:    "text",
		})
	}

	return acct
}

// newAPIPlanLabel returns the plan title, falling back to "订阅 #id" when the
// panel omits or cannot resolve the title.
func newAPIPlanLabel(s newAPIUserSubscription) string {
	if title := strings.TrimSpace(s.PlanTitle); title != "" {
		return title
	}
	return fmt.Sprintf("订阅 #%d", s.ID)
}

func newAPIExpirySuffix(days int) string {
	if days <= 0 {
		return ""
	}
	return fmt.Sprintf(" · %d 天后到期", days)
}

func newAPIResetSuffix(nextReset int64) string {
	if nextReset <= 0 {
		return ""
	}
	days := int((nextReset - time.Now().Unix() + 86399) / 86400)
	if days <= 0 {
		return ""
	}
	return fmt.Sprintf(" · %d 天后重置", days)
}

// ---------------------------------------------------------------------------
// API keys (token list) — names and usage only
//
// GET /api/token/ (upstream: controller.GetAllTokens) returns the caller's
// tokens with their quota counters. The "key" field is deliberately NOT part
// of newAPITokenItem: the decoder drops it, so a full key can never reach the
// cache, the SQLite file, the JSON response or the dashboard. We never call
// POST /api/token/:id/key (the reveal endpoint) either.
// ---------------------------------------------------------------------------

type newAPITokenItem struct {
	ID             int64   `json:"id"`
	Name           string  `json:"name"`
	Status         int     `json:"status"`
	CreatedTime    int64   `json:"created_time"`
	AccessedTime   int64   `json:"accessed_time"`
	ExpiredTime    int64   `json:"expired_time"`
	RemainQuota    float64 `json:"remain_quota"`
	UnlimitedQuota bool    `json:"unlimited_quota"`
	UsedQuota      float64 `json:"used_quota"`
	Group          string  `json:"group"`
}

type newAPIKeyWindow struct {
	Used       float64 `json:"used"`
	Remaining  float64 `json:"remaining"`
	Unlimited  bool    `json:"unlimited"`
	Total      float64 `json:"total"`
	Unit       string  `json:"unit"`
	StatusText string  `json:"status_text"`
}

// newAPIKey is the safe, display-only projection of a panel token.
type newAPIKey struct {
	ID             int64            `json:"id"`
	Name           string           `json:"name"`
	Status         string           `json:"status"`
	Group          string           `json:"group,omitempty"`
	Unlimited      bool             `json:"unlimited"`
	CreatedAt      string           `json:"created_at,omitempty"`
	AccessedAt     string           `json:"accessed_at,omitempty"`
	ExpiresAt      string           `json:"expires_at,omitempty"`
	UsedUSD        float64          `json:"used_usd"`
	RemainingUSD   float64          `json:"remaining_usd"`
	TotalUSD       float64          `json:"total_usd"`
	PanelUsed      string           `json:"panel_used,omitempty"`
	PanelRemaining string           `json:"panel_remaining,omitempty"`
	StatusText     string           `json:"status_text,omitempty"`
	Window         *newAPIKeyWindow `json:"window,omitempty"`
}

func newAPITokenStatusText(status int) string {
	switch status {
	case 1:
		return "启用"
	case 2:
		return "已禁用"
	case 3:
		return "已过期"
	case 4:
		return "额度耗尽"
	default:
		return "未知"
	}
}

// fetchNewAPITokens pages through GET /api/token/ and returns names + usage.
func fetchNewAPITokens(base, token string) ([]newAPITokenItem, error) {
	const pageSize = 100
	var all []newAPITokenItem
	for page := 1; page <= 100; page++ { // hard stop; 100 pages × 100 = 10k keys
		url := fmt.Sprintf("%s/api/token/?p=%d&page_size=%d", base, page, pageSize)
		var r struct {
			Data struct {
				Total int               `json:"total"`
				Items []newAPITokenItem `json:"items"`
			} `json:"data"`
		}
		if err := newAPIGetJSON(url, token, &r); err != nil {
			return nil, err
		}
		all = append(all, r.Data.Items...)
		if len(r.Data.Items) < pageSize || len(all) >= r.Data.Total {
			break
		}
	}
	return all, nil
}

// buildNewAPIKeys projects tokens to the safe response. All quota values are
// converted with the same quota_per_unit used by the account card.
func buildNewAPIKeys(items []newAPITokenItem, snap *newAPISnapshot) []newAPIKey {
	units := snap.QuotaPerUnit
	if units <= 0 {
		units = snap.Panel.QuotaPerUnit
	}
	if units <= 0 {
		units = 1
	}
	now := time.Now()
	keys := make([]newAPIKey, 0, len(items))
	for _, it := range items {
		remaining := it.RemainQuota
		if it.UnlimitedQuota || remaining < 0 {
			// Unlimited tokens carry a negative remain_quota upstream (it is a
			// credit counter, not a balance). Never surface a negative balance.
			remaining = 0
		}
		k := newAPIKey{
			ID:           it.ID,
			Name:         it.Name,
			Status:       newAPITokenStatusText(it.Status),
			Group:        strings.TrimSpace(it.Group),
			Unlimited:    it.UnlimitedQuota,
			UsedUSD:      it.UsedQuota / units,
			RemainingUSD: remaining / units,
		}
		if it.CreatedTime > 0 {
			k.CreatedAt = time.Unix(it.CreatedTime, 0).UTC().Format(time.RFC3339)
		}
		if it.AccessedTime > 0 {
			k.AccessedAt = time.Unix(it.AccessedTime, 0).UTC().Format(time.RFC3339)
		}
		if it.ExpiredTime > 0 {
			k.ExpiresAt = time.Unix(it.ExpiredTime, 0).UTC().Format(time.RFC3339)
			if it.ExpiredTime <= now.Unix() {
				k.Status = "已过期"
			}
		}
		if used, ok := snap.Panel.quotaToDisplay(it.UsedQuota, units); ok {
			k.PanelUsed = used
		}
		if !it.UnlimitedQuota {
			k.TotalUSD = k.UsedUSD + k.RemainingUSD
			if rem, ok := snap.Panel.quotaToDisplay(remaining, units); ok {
				k.PanelRemaining = rem
			}
		} else {
			k.StatusText = "不限量"
		}
		if k.StatusText == "" {
			k.StatusText = fmt.Sprintf("已用 $%.2f · 余 $%.2f", k.UsedUSD, k.RemainingUSD)
		}

		// Window matches the account card's shape so the dashboard can render a
		// progress bar with the shared quota-window CSS.
		w := &newAPIKeyWindow{
			Used:      k.UsedUSD,
			Remaining: k.RemainingUSD,
			Unit:      "USD",
			Unlimited: it.UnlimitedQuota,
		}
		if !it.UnlimitedQuota {
			w.Total = k.TotalUSD
			w.StatusText = k.StatusText
		} else {
			w.StatusText = "不限量"
		}
		k.Window = w

		keys = append(keys, k)
	}
	// Most-consumed first: that is what a usage view is for.
	sort.SliceStable(keys, func(i, j int) bool {
		if keys[i].UsedUSD == keys[j].UsedUSD {
			return keys[i].ID > keys[j].ID
		}
		return keys[i].UsedUSD > keys[j].UsedUSD
	})
	return keys
}

// ---------------------------------------------------------------------------
// State + lifecycle
// ---------------------------------------------------------------------------

func newAPIAccountCache(name string) quotaAccount {
	return quotaAccount{
		Type: "newapi", Name: name, Success: false,
		Windows: []quotaWindow{}, Error: "not fetched yet",
	}
}

func initNewAPIAccounts(cfgs []newAPIAccountCfg) {
	newAPIAcctMu.Lock()
	seen := make(map[string]bool, len(newAPIAccounts))
	for _, a := range newAPIAccounts {
		seen[a.Name] = true
	}
	for _, cfg := range cfgs {
		name := strings.TrimSpace(cfg.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		newAPIAccounts = append(newAPIAccounts, &newAPIAccountRuntime{
			Name:    name,
			BaseURL: normalizeNewAPIBaseURL(cfg.BaseURL),
			Token:   strings.TrimSpace(cfg.Token),
			Cache:   newAPIAccountCache(name),
		})
	}
	accounts := append([]*newAPIAccountRuntime(nil), newAPIAccounts...)
	newAPIAcctMu.Unlock()

	for _, a := range accounts {
		if a.Token != "" {
			go newAPIRefresh(a)
		}
	}
}

// credentials snapshots the mutable connection fields under the same lock used
// by settoken, so the refresh goroutine never races with an edit.
func (a *newAPIAccountRuntime) credentials() (baseURL, token string) {
	newAPIAcctMu.RLock()
	defer newAPIAcctMu.RUnlock()
	return a.BaseURL, a.Token
}

func refreshNewAPIQuota(acct *newAPIAccountRuntime) {
	baseURL, token := acct.credentials()
	snap, warnings, err := fetchNewAPISnapshot(baseURL, token)
	acct.CacheMu.Lock()
	defer acct.CacheMu.Unlock()
	if err != nil {
		acct.Cache.Success = false
		acct.Cache.Windows = nil
		acct.Cache.Plan = ""
		acct.Cache.Error = err.Error()
		acct.Cache.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		return
	}
	next := buildNewAPIQuotaAccount(acct.Name, snap)
	// Partial failures (e.g. a token without profile:read) still render the
	// windows that did load; the note explains what is missing.
	next.Error = strings.Join(warnings, "；")
	acct.Cache = next
}

func findNewAPIAccount(name string) *newAPIAccountRuntime {
	newAPIAcctMu.RLock()
	defer newAPIAcctMu.RUnlock()
	for _, a := range newAPIAccounts {
		if a.Name == name {
			return a
		}
	}
	return nil
}

// newAPIHeaderValue returns a case-insensitive header value. Headers may be
// passed either with canonical names or raw lower-case keys.
func newAPIHeaderValue(headers map[string][]string, name string) string {
	for key, values := range headers {
		if !strings.EqualFold(key, name) || len(values) == 0 {
			continue
		}
		return strings.TrimSpace(values[0])
	}
	return ""
}

// applyNewAPIAccountAction implements addaccount / removeaccount / settoken /
// refresh for both the GET (query) and POST (JSON) handlers.
func applyNewAPIAccountAction(action, name, token, baseURL string) pluginapi.ManagementResponse {
	action = strings.ToLower(strings.TrimSpace(action))
	name = strings.TrimSpace(name)
	token = strings.TrimSpace(token)
	baseURL = strings.TrimSpace(baseURL)

	switch action {
	case "addaccount":
		if name == "" {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "account name is required"})
		}
		if token == "" {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "access token is required"})
		}
		newAPIAcctMu.Lock()
		for _, a := range newAPIAccounts {
			if a.Name == name {
				newAPIAcctMu.Unlock()
				return jsonResponse(http.StatusConflict, map[string]string{"error": "already exists"})
			}
		}
		r := &newAPIAccountRuntime{
			Name:    name,
			BaseURL: normalizeNewAPIBaseURL(baseURL),
			Token:   token,
			Cache:   newAPIAccountCache(name),
		}
		newAPIAccounts = append(newAPIAccounts, r)
		newAPIAcctMu.Unlock()
		persistNewAPIAccount(name, r.BaseURL, token)
		go newAPIRefresh(r)
		return jsonResponse(http.StatusOK, map[string]any{"added": true, "account": name})
	case "removeaccount":
		if name == "" {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "account name is required"})
		}
		newAPIAcctMu.Lock()
		for i, a := range newAPIAccounts {
			if a.Name == name {
				newAPIAccounts = append(newAPIAccounts[:i], newAPIAccounts[i+1:]...)
				break
			}
		}
		newAPIAcctMu.Unlock()
		deleteNewAPIAccountFromDB(name)
		return jsonResponse(http.StatusOK, map[string]any{"removed": true, "account": name})
	case "settoken", "setkey":
		target := findNewAPIAccount(name)
		if target == nil {
			return jsonResponse(http.StatusNotFound, map[string]string{"error": "not found"})
		}
		// Either field may be rotated on its own: an empty token keeps the
		// stored one, so the base URL can be corrected without re-entering it.
		if token == "" && baseURL == "" {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "access token or base_url is required"})
		}
		newAPIAcctMu.Lock()
		if token != "" {
			target.Token = token
		}
		if baseURL != "" {
			target.BaseURL = normalizeNewAPIBaseURL(baseURL)
		}
		base, tok := target.BaseURL, target.Token
		newAPIAcctMu.Unlock()
		persistNewAPIAccount(name, base, tok)
		go newAPIRefresh(target)
		return jsonResponse(http.StatusOK, map[string]any{"updated": true, "account": name})
	case "refresh":
		target := findNewAPIAccount(name)
		if target == nil {
			return jsonResponse(http.StatusNotFound, map[string]string{"error": "not found"})
		}
		if _, tok := target.credentials(); tok == "" {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "no access token"})
		}
		go newAPIRefresh(target)
		return jsonResponse(http.StatusOK, map[string]any{"refreshing": true, "account": name})
	default:
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "unknown action"})
	}
}

func handleNewAPIQuotaGet(query map[string][]string, headers map[string][]string) pluginapi.ManagementResponse {
	if action, ok := getQueryParam(query, "action"); ok && strings.TrimSpace(action) != "" {
		name, _ := getQueryParam(query, "account")
		if strings.EqualFold(strings.TrimSpace(action), "keys") {
			return handleNewAPIKeys(name)
		}
		// Credentials are read from headers first: CPA logs the full request
		// URI, so a token in the query string would leak into the access log
		// (and browser history / proxy logs). The resource router only wires
		// GET, so headers are the safe channel for mutating actions too.
		token := newAPIHeaderValue(headers, "X-NewAPI-Token")
		if token == "" {
			token, _ = getQueryParam(query, "token")
		}
		if token == "" {
			// Backwards-friendly alias used by the other providers.
			token, _ = getQueryParam(query, "api_key")
		}
		baseURL := newAPIHeaderValue(headers, "X-NewAPI-Base-URL")
		if baseURL == "" {
			baseURL, _ = getQueryParam(query, "base_url")
		}
		return applyNewAPIAccountAction(action, name, token, baseURL)
	}

	newAPIAcctMu.RLock()
	accounts := make([]quotaAccount, 0, len(newAPIAccounts))
	for _, a := range newAPIAccounts {
		a.CacheMu.RLock()
		acct := a.Cache
		a.CacheMu.RUnlock()
		acct.Name = a.Name
		acct.Type = "newapi"
		acct.BaseURL = a.BaseURL
		accounts = append(accounts, acct)
	}
	newAPIAcctMu.RUnlock()
	return jsonResponse(http.StatusOK, quotaResponse{Accounts: accounts})
}

// handleNewAPIKeys returns the account's key list: names, status and usage.
// The upstream "key" field is never decoded, so no secret can be returned.
func handleNewAPIKeys(name string) pluginapi.ManagementResponse {
	target := findNewAPIAccount(strings.TrimSpace(name))
	if target == nil {
		return jsonResponse(http.StatusNotFound, map[string]string{"error": "account not found"})
	}
	base, token := target.credentials()
	if token == "" {
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "no access token"})
	}
	items, err := fetchNewAPITokens(base, token)
	if err != nil {
		return jsonResponse(http.StatusOK, map[string]any{
			"account": target.Name,
			"keys":    []newAPIKey{},
			"error":   "API Key 列表读取失败（需 api_key:read）：" + err.Error(),
		})
	}
	// Only the public panel settings are needed for unit conversion; avoid a
	// full quota refresh just to open the key list.
	panel := fetchNewAPIPanelInfo(base)
	snap := &newAPISnapshot{QuotaPerUnit: panel.QuotaPerUnit, Panel: panel}
	keys := buildNewAPIKeys(items, snap)
	return jsonResponse(http.StatusOK, map[string]any{
		"account":  target.Name,
		"base_url": base,
		"keys":     keys,
		"total":    len(keys),
	})
}

func handleNewAPIQuotaPost(body []byte) pluginapi.ManagementResponse {
	var req struct {
		Action  string `json:"action"`
		Account string `json:"account"`
		Token   string `json:"token"`
		APIKey  string `json:"api_key"`
		BaseURL string `json:"base_url"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
	}
	token := req.Token
	if token == "" {
		token = req.APIKey
	}
	return applyNewAPIAccountAction(req.Action, req.Account, token, req.BaseURL)
}

// ---------------------------------------------------------------------------
// DB persistence (token obfuscated at rest, like the Colab refresh token)
// ---------------------------------------------------------------------------

func loadNewAPIAccountsFromDB() {
	defer func() { recover() }()

	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil {
		return
	}
	rows, err := d.Query("SELECT name, base_url, token FROM newapi_accounts")
	if err != nil {
		return
	}
	defer rows.Close()

	newAPIAcctMu.Lock()
	for rows.Next() {
		var name, baseURL, token string
		if rows.Scan(&name, &baseURL, &token) != nil {
			continue
		}
		seen := false
		for _, a := range newAPIAccounts {
			if a.Name == name {
				seen = true
				break
			}
		}
		if seen {
			continue
		}
		newAPIAccounts = append(newAPIAccounts, &newAPIAccountRuntime{
			Name:    name,
			BaseURL: normalizeNewAPIBaseURL(baseURL),
			Token:   deobfuscateToken(token),
			Cache:   newAPIAccountCache(name),
		})
	}
	pending := append([]*newAPIAccountRuntime(nil), newAPIAccounts...)
	newAPIAcctMu.Unlock()

	for _, a := range pending {
		if a.Token != "" {
			go newAPIRefresh(a)
		}
	}
}

func persistNewAPIAccount(name, baseURL, token string) {
	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil {
		return
	}
	d.Exec(`INSERT INTO newapi_accounts (name, base_url, token) VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET base_url=excluded.base_url, token=excluded.token`,
		name, baseURL, obfuscateToken(token))
}

func deleteNewAPIAccountFromDB(name string) {
	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil {
		return
	}
	d.Exec("DELETE FROM newapi_accounts WHERE name = ?", name)
}
