package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// ---------------------------------------------------------------------------
// Ollama Cloud quota monitoring
//
// Ported from the reference implementation:
//   https://github.com/jacklee-code/ollama-cloud-quota-monitor
//
// Ollama Cloud does not expose a public quota API. Instead we fetch the
// https://ollama.com/settings page with the user's session cookie and parse
// the "Cloud usage" block out of the returned HTML.
// ---------------------------------------------------------------------------

const (
	ollamaSettingsURL  = "https://ollama.com/settings"
	ollamaUserAgent    = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	ollamaHTTPTimeout  = 20 * time.Second
	ollamaMaxHTMLBytes = 4 << 20 // 4 MB
	ollamaLabelSession = "Session"
	ollamaLabelWeekly  = "Weekly"
)

var (
	reOllamaCloudUsageBlock = regexp.MustCompile(`(?is)<span>Cloud usage</span>(.*?)</div>\s*<script>`)
	reOllamaPlan            = regexp.MustCompile(`(?is)rounded-full[^>]*capitalize[^>]*>\s*([^<]+?)\s*</span`)
	reOllamaPctUsed         = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*%\s*used`)
	reOllamaUsageTrack      = regexp.MustCompile(`(?is)data-usage-track[^>]*aria-label="([^"]+)"[^>]*>(.*?)</div>`)
	reOllamaUsageSegment    = regexp.MustCompile(`(?is)<button\b[^>]*data-usage-segment[^>]*>`)
	reOllamaModel           = regexp.MustCompile(`data-model="([^"]+)"`)
	reOllamaRequests        = regexp.MustCompile(`data-requests="(\d+)"`)
	reOllamaWidth           = regexp.MustCompile(`width:\s*([\d.]+)%`)
	reOllamaPeriodHeader    = regexp.MustCompile(`(?s)<div class="flex justify-between mb-2">(.*?)</div>`)
	reOllamaHeaderSpan      = regexp.MustCompile(`(?s)<span class="text-sm[^"]*"[^>]*>\s*([^<]+?)\s*</span`)
	reOllamaResetInfo       = regexp.MustCompile(`(?is)class="[^"]*local-time[^"]*"[^>]*data-time="([^"]+)"[^>]*>\s*([^<]+?)\s*</div>`)
	reOllamaNotLoggedIn     = regexp.MustCompile(`(?i)(sign in|log in|invalid credentials)`)
)

// ---------------------------------------------------------------------------
// State + lifecycle
// ---------------------------------------------------------------------------

type ollamaAccountRuntime struct {
	Name          string
	SessionCookie string
	ShowSession   bool
	ShowWeekly    bool
	Cache         quotaAccount
	CacheMu       sync.RWMutex
}

var (
	ollamaAccounts   []*ollamaAccountRuntime
	ollamaAcctMu     sync.RWMutex
	ollamaHTTPClient = &http.Client{Timeout: ollamaHTTPTimeout}
)

func initOllamaAccounts(cfgs []ollamaAcctCfg) {
	ollamaAcctMu.Lock()
	defer ollamaAcctMu.Unlock()

	seen := make(map[string]bool)
	for _, a := range ollamaAccounts {
		seen[a.Name] = true
	}
	for _, cfg := range cfgs {
		name := strings.TrimSpace(cfg.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		ollamaAccounts = append(ollamaAccounts, &ollamaAccountRuntime{
			Name:          name,
			SessionCookie: strings.TrimSpace(cfg.SessionCookie),
			ShowSession:   cfg.ShowSession,
			ShowWeekly:    cfg.ShowWeekly,
			Cache: quotaAccount{
				Type:    "ollama",
				Name:    name,
				Success: false,
				Windows: []quotaWindow{},
				Error:   "not fetched yet",
			},
		})
	}

	// Kick off immediate fetch for any account that has a cookie
	for _, a := range ollamaAccounts {
		if a.SessionCookie != "" {
			go refreshOllamaQuota(a)
		}
	}
}

func refreshOllamaQuota(acct *ollamaAccountRuntime) {
	quota, err := fetchOllamaQuota(acct.SessionCookie, acct.ShowSession, acct.ShowWeekly)
	acct.CacheMu.Lock()
	defer acct.CacheMu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		acct.Cache.Success = false
		acct.Cache.Windows = nil
		acct.Cache.Plan = ""
		acct.Cache.Error = err.Error()
		acct.Cache.UpdatedAt = now
		return
	}
	quota.Name = acct.Name
	quota.Type = "ollama"
	acct.Cache = *quota
}

// ---------------------------------------------------------------------------
// HTTP + Parse
// ---------------------------------------------------------------------------

// buildOllamaCookieHeader normalizes the session cookie. It accepts either a
// full "aid=...; __Secure-session=..." string, a "Cookie: ..." header, or a
// bare session value (in which case it is wrapped as __Secure-session=...).
func buildOllamaCookieHeader(sessionCookie string) string {
	cookie := strings.TrimSpace(sessionCookie)
	if strings.HasPrefix(strings.ToLower(cookie), "cookie:") {
		cookie = strings.TrimSpace(cookie[7:])
	}
	if cookie == "" {
		return ""
	}
	if !strings.Contains(cookie, "=") {
		return "__Secure-session=" + cookie
	}
	return strings.TrimRight(cookie, ";")
}

func extractOllamaCloudUsageBlock(html string) (string, error) {
	m := reOllamaCloudUsageBlock.FindStringSubmatch(html)
	if m == nil {
		return "", fmt.Errorf("页面中未找到 Cloud usage 区块（可能未登录或页面结构已变更）")
	}
	return m[1], nil
}

func parseOllamaPlan(block string) string {
	m := reOllamaPlan.FindStringSubmatch(block)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func parseOllamaPercentFromAria(ariaLabel string) (float64, bool) {
	m := reOllamaPctUsed.FindStringSubmatch(ariaLabel)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

type ollamaUsageTrack struct {
	ariaLabel   string
	usedPercent float64
	hasPercent  bool
	models      []ollamaModelUsage
}

func parseOllamaUsageTracks(block string) []ollamaUsageTrack {
	var tracks []ollamaUsageTrack
	for _, trackMatch := range reOllamaUsageTrack.FindAllStringSubmatch(block, -1) {
		ariaLabel := trackMatch[1]
		inner := trackMatch[2]

		var models []ollamaModelUsage
		for _, seg := range reOllamaUsageSegment.FindAllString(inner, -1) {
			modelMatch := reOllamaModel.FindStringSubmatch(seg)
			requestsMatch := reOllamaRequests.FindStringSubmatch(seg)
			if modelMatch == nil || requestsMatch == nil {
				continue
			}
			requests, _ := strconv.ParseInt(requestsMatch[1], 10, 64)
			mu := ollamaModelUsage{
				Model:    modelMatch[1],
				Requests: requests,
			}
			if widthMatch := reOllamaWidth.FindStringSubmatch(seg); widthMatch != nil {
				if w, err := strconv.ParseFloat(widthMatch[1], 64); err == nil {
					mu.SharePercent = w
				}
			}
			models = append(models, mu)
		}

		pct, ok := parseOllamaPercentFromAria(ariaLabel)
		tracks = append(tracks, ollamaUsageTrack{
			ariaLabel:   ariaLabel,
			usedPercent: pct,
			hasPercent:  ok,
			models:      models,
		})
	}
	return tracks
}

func parseOllamaPeriodHeaders(block string) []string {
	var headers []string
	for _, section := range reOllamaPeriodHeader.FindAllStringSubmatch(block, -1) {
		spans := reOllamaHeaderSpan.FindAllStringSubmatch(section[1], -1)
		if len(spans) >= 2 {
			headers = append(headers, strings.TrimSpace(spans[1][1]))
		}
		if len(headers) >= 2 {
			break
		}
	}
	for len(headers) < 2 {
		headers = append(headers, "")
	}
	return headers[:2]
}

func parseOllamaResetInfo(block string) []string {
	var resets []string
	for _, item := range reOllamaResetInfo.FindAllStringSubmatch(block, -1) {
		resets = append(resets, item[1])
	}
	for len(resets) < 2 {
		resets = append(resets, "")
	}
	return resets[:2]
}

func parseOllamaResetAt(value string) (time.Time, bool) {
	text := strings.TrimSpace(value)
	if text == "" {
		return time.Time{}, false
	}
	if strings.HasSuffix(text, "Z") {
		text = text[:len(text)-1] + "+00:00"
	}
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

func clampOllamaPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func buildOllamaWindow(
	label string,
	usedPercent float64,
	hasPercent bool,
	statusText string,
	resetAtRaw string,
	models []ollamaModelUsage,
	now time.Time,
) *quotaWindow {
	if !hasPercent {
		return nil
	}
	used := clampOllamaPercent(usedPercent)
	resetAt := ""
	resetInSec := 0
	if resetAtDT, ok := parseOllamaResetAt(resetAtRaw); ok {
		resetInSec = int(resetAtDT.Sub(now).Seconds())
		if resetInSec < 0 {
			resetInSec = 0
		}
		resetAt = resetAtDT.Format(time.RFC3339)
	}
	return &quotaWindow{
		Label:      label,
		Used:       used,
		Remaining:  100.0 - used,
		Total:      100.0,
		Unit:       "%",
		ResetAt:    resetAt,
		ResetInSec: resetInSec,
		StatusText: statusText,
		Models:     models,
	}
}

func parseOllamaQuotaHTML(html string, now time.Time) (string, []quotaWindow, error) {
	if reOllamaNotLoggedIn.MatchString(html) {
		if !strings.Contains(html, "Cloud usage") {
			return "", nil, fmt.Errorf("未登录或 cookie 无效")
		}
	}

	block, err := extractOllamaCloudUsageBlock(html)
	if err != nil {
		return "", nil, err
	}
	plan := parseOllamaPlan(block)
	tracks := parseOllamaUsageTracks(block)
	statusTexts := parseOllamaPeriodHeaders(block)
	resets := parseOllamaResetInfo(block)

	labels := []string{ollamaLabelSession, ollamaLabelWeekly}
	var windows []quotaWindow
	for i, label := range labels {
		var track ollamaUsageTrack
		if i < len(tracks) {
			track = tracks[i]
		}
		statusText := ""
		if i < len(statusTexts) {
			statusText = statusTexts[i]
		}
		resetAtRaw := ""
		if i < len(resets) {
			resetAtRaw = resets[i]
		}
		if w := buildOllamaWindow(label, track.usedPercent, track.hasPercent, statusText, resetAtRaw, track.models, now); w != nil {
			windows = append(windows, *w)
		}
	}

	if len(windows) == 0 {
		return "", nil, fmt.Errorf("无法从 settings 页面解析 Cloud usage 数据")
	}
	return plan, windows, nil
}

func fetchOllamaQuota(sessionCookie string, showSession, showWeekly bool) (*quotaAccount, error) {
	now := time.Now().UTC()
	updatedAt := now.Format(time.RFC3339)

	if strings.TrimSpace(sessionCookie) == "" {
		return nil, fmt.Errorf("未配置 session_cookie")
	}

	cookieHeader := buildOllamaCookieHeader(sessionCookie)
	if cookieHeader == "" {
		return nil, fmt.Errorf("session_cookie 无效")
	}

	req, err := http.NewRequest(http.MethodGet, ollamaSettingsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", cookieHeader)
	req.Header.Set("User-Agent", ollamaUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := ollamaHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("认证失败 (HTTP %d)，请检查 session cookie", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("settings 页面返回 HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, ollamaMaxHTMLBytes))
	if err != nil {
		return nil, fmt.Errorf("read body failed: %w", err)
	}

	plan, windows, err := parseOllamaQuotaHTML(string(body), now)
	if err != nil {
		return nil, err
	}

	filtered := make([]quotaWindow, 0, len(windows))
	for _, w := range windows {
		if w.Label == ollamaLabelSession && !showSession {
			continue
		}
		if w.Label == ollamaLabelWeekly && !showWeekly {
			continue
		}
		filtered = append(filtered, w)
	}

	return &quotaAccount{
		Type:      "ollama",
		Success:   true,
		UpdatedAt: updatedAt,
		Plan:      plan,
		Windows:   filtered,
	}, nil
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func handleOllamaQuotaGet(query map[string][]string) pluginapi.ManagementResponse {
	if action, ok := getQueryParam(query, "action"); ok && action != "" {
		acctName, _ := getQueryParam(query, "account")
		cookie, _ := getQueryParam(query, "cookie")
		showSession, _ := getQueryParam(query, "show_session")
		showWeekly, _ := getQueryParam(query, "show_weekly")
		action = strings.ToLower(strings.TrimSpace(action))
		acctName = strings.TrimSpace(acctName)

		switch action {
		case "addaccount":
			if acctName == "" {
				return jsonResponse(http.StatusBadRequest, map[string]string{"error": "account name is required"})
			}
			ollamaAcctMu.Lock()
			for _, a := range ollamaAccounts {
				if a.Name == acctName {
					ollamaAcctMu.Unlock()
					return jsonResponse(http.StatusConflict, map[string]string{"error": "account already exists"})
				}
			}
			r := &ollamaAccountRuntime{
				Name:          acctName,
				SessionCookie: strings.TrimSpace(cookie),
				ShowSession:   parseBoolParam(showSession, true),
				ShowWeekly:    parseBoolParam(showWeekly, true),
				Cache: quotaAccount{
					Type:    "ollama",
					Name:    acctName,
					Success: false,
					Windows: []quotaWindow{},
					Error:   "not fetched yet",
				},
			}
			ollamaAccounts = append(ollamaAccounts, r)
			ollamaAcctMu.Unlock()
			persistOllamaAccount(acctName, r.SessionCookie, r.ShowSession, r.ShowWeekly)
			if r.SessionCookie != "" {
				go refreshOllamaQuota(r)
			}
		case "removeaccount":
			ollamaAcctMu.Lock()
			for i, a := range ollamaAccounts {
				if a.Name == acctName {
					ollamaAccounts = append(ollamaAccounts[:i], ollamaAccounts[i+1:]...)
					break
				}
			}
			ollamaAcctMu.Unlock()
			deleteOllamaAccountFromDB(acctName)
		case "setcookie":
			ollamaAcctMu.RLock()
			var target *ollamaAccountRuntime
			for _, a := range ollamaAccounts {
				if a.Name == acctName {
					target = a
					break
				}
			}
			ollamaAcctMu.RUnlock()
			if target == nil {
				return jsonResponse(http.StatusNotFound, map[string]string{"error": "account not found"})
			}
			c := strings.TrimSpace(cookie)
			if c == "" {
				return jsonResponse(http.StatusBadRequest, map[string]string{"error": "cookie is required"})
			}
			target.SessionCookie = c
			if showSession != "" {
				target.ShowSession = parseBoolParam(showSession, true)
			}
			if showWeekly != "" {
				target.ShowWeekly = parseBoolParam(showWeekly, true)
			}
			persistOllamaAccount(target.Name, target.SessionCookie, target.ShowSession, target.ShowWeekly)
			go refreshOllamaQuota(target)
		case "refresh":
			ollamaAcctMu.RLock()
			var target *ollamaAccountRuntime
			for _, a := range ollamaAccounts {
				if a.Name == acctName {
					target = a
					break
				}
			}
			ollamaAcctMu.RUnlock()
			if target == nil {
				return jsonResponse(http.StatusNotFound, map[string]string{"error": "account not found"})
			}
			if target.SessionCookie == "" {
				return jsonResponse(http.StatusBadRequest, map[string]string{"error": "no session cookie configured"})
			}
			go refreshOllamaQuota(target)
		default:
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "unknown action"})
		}
	}

	ollamaAcctMu.RLock()
	accounts := make([]quotaAccount, 0, len(ollamaAccounts))
	for _, a := range ollamaAccounts {
		a.CacheMu.RLock()
		acct := a.Cache
		a.CacheMu.RUnlock()
		acct.Name = a.Name
		acct.Type = "ollama"
		accounts = append(accounts, acct)
	}
	ollamaAcctMu.RUnlock()
	return jsonResponse(http.StatusOK, quotaResponse{Accounts: accounts})
}

func handleOllamaQuotaPost(body []byte) pluginapi.ManagementResponse {
	var req struct {
		Action      string `json:"action"`
		Account     string `json:"account"`
		Cookie      string `json:"cookie"`
		ShowSession *bool  `json:"show_session"`
		ShowWeekly  *bool  `json:"show_weekly"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
	}
	acctName := strings.TrimSpace(req.Account)
	action := strings.ToLower(strings.TrimSpace(req.Action))

	if action == "addaccount" {
		cookie := strings.TrimSpace(req.Cookie)
		if acctName == "" {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "account name is required"})
		}
		ollamaAcctMu.Lock()
		for _, a := range ollamaAccounts {
			if a.Name == acctName {
				ollamaAcctMu.Unlock()
				return jsonResponse(http.StatusConflict, map[string]string{"error": "account already exists"})
			}
		}
		showSession := true
		if req.ShowSession != nil {
			showSession = *req.ShowSession
		}
		showWeekly := true
		if req.ShowWeekly != nil {
			showWeekly = *req.ShowWeekly
		}
		r := &ollamaAccountRuntime{
			Name:          acctName,
			SessionCookie: cookie,
			ShowSession:   showSession,
			ShowWeekly:    showWeekly,
			Cache: quotaAccount{
				Type:    "ollama",
				Name:    acctName,
				Success: false,
				Windows: []quotaWindow{},
				Error:   "not fetched yet",
			},
		}
		ollamaAccounts = append(ollamaAccounts, r)
		ollamaAcctMu.Unlock()
		persistOllamaAccount(acctName, cookie, showSession, showWeekly)
		if cookie != "" {
			go refreshOllamaQuota(r)
		}
		return handleOllamaQuotaGet(nil)
	}

	if acctName == "" {
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "account name is required"})
	}

	ollamaAcctMu.RLock()
	var target *ollamaAccountRuntime
	for _, a := range ollamaAccounts {
		if a.Name == acctName {
			target = a
			break
		}
	}
	ollamaAcctMu.RUnlock()

	if target == nil {
		return jsonResponse(http.StatusNotFound, map[string]string{"error": "account not found"})
	}

	switch action {
	case "removeaccount":
		ollamaAcctMu.Lock()
		for i, a := range ollamaAccounts {
			if a.Name == acctName {
				ollamaAccounts = append(ollamaAccounts[:i], ollamaAccounts[i+1:]...)
				break
			}
		}
		ollamaAcctMu.Unlock()
		deleteOllamaAccountFromDB(acctName)
		return handleOllamaQuotaGet(nil)
	case "setcookie":
		cookie := strings.TrimSpace(req.Cookie)
		if cookie == "" {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "cookie is required"})
		}
		target.SessionCookie = cookie
		if req.ShowSession != nil {
			target.ShowSession = *req.ShowSession
		}
		if req.ShowWeekly != nil {
			target.ShowWeekly = *req.ShowWeekly
		}
		persistOllamaAccount(target.Name, target.SessionCookie, target.ShowSession, target.ShowWeekly)
		go refreshOllamaQuota(target)
		return handleOllamaQuotaGet(nil)
	case "refresh":
		if target.SessionCookie == "" {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "no session cookie configured for this account"})
		}
		go refreshOllamaQuota(target)
		return handleOllamaQuotaGet(nil)
	default:
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "unknown action, use 'setcookie', 'refresh', 'addaccount', or 'removeaccount'"})
	}
}

func parseBoolParam(s string, def bool) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return def
	}
	switch s {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

// ---------------------------------------------------------------------------
// DB persistence for Ollama accounts
// ---------------------------------------------------------------------------

func loadOllamaAccountsFromDB() {
	defer func() { recover() }()

	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil {
		return
	}
	rows, err := d.Query("SELECT name, session_cookie, show_session, show_weekly FROM ollama_accounts")
	if err != nil {
		return
	}
	defer rows.Close()
	ollamaAcctMu.Lock()
	defer ollamaAcctMu.Unlock()
	for rows.Next() {
		var name, cookie string
		var showSession, showWeekly int
		if rows.Scan(&name, &cookie, &showSession, &showWeekly) != nil {
			continue
		}
		seen := false
		for _, a := range ollamaAccounts {
			if a.Name == name {
				seen = true
				break
			}
		}
		if seen {
			continue
		}
		ollamaAccounts = append(ollamaAccounts, &ollamaAccountRuntime{
			Name:          name,
			SessionCookie: cookie,
			ShowSession:   showSession != 0,
			ShowWeekly:    showWeekly != 0,
			Cache: quotaAccount{
				Type:    "ollama",
				Name:    name,
				Success: false,
				Windows: []quotaWindow{},
				Error:   "not fetched yet",
			},
		})
		if cookie != "" {
			go refreshOllamaQuota(ollamaAccounts[len(ollamaAccounts)-1])
		}
	}
}

func persistOllamaAccount(name, cookie string, showSession, showWeekly bool) {
	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil {
		return
	}
	d.Exec(`INSERT INTO ollama_accounts (name, session_cookie, show_session, show_weekly) VALUES (?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET session_cookie=excluded.session_cookie, show_session=excluded.show_session, show_weekly=excluded.show_weekly`,
		name, cookie, boolToInt(showSession), boolToInt(showWeekly))
}

func deleteOllamaAccountFromDB(name string) {
	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil {
		return
	}
	d.Exec("DELETE FROM ollama_accounts WHERE name = ?", name)
}
