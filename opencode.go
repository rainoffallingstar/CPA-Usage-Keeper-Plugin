package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	opencodeDashboardBase = "https://opencode.ai/workspace"
	opencodeServerID      = "def39973159c7f0483d8793a822b8dbb10d067e12c65455fcb4608459ba0234f"
	quotaUserAgent        = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Gecko/20100101 Firefox/148.0"
	quotaHTTPTimeout      = 10 * time.Second
	maxQuotaHTMLBytes     = 4 << 20 // 4 MB
)

var (
	reWorkspaceEntry = regexp.MustCompile(`id\s*:\s*"(wrk_[^"]+)"[^{}]*?name\s*:\s*"([^"]*)"`)
	reRollingPct  = regexp.MustCompile(`rollingUsage:\s*\$R\[\d+\]\s*=\s*\{[^}]*usagePercent\s*:\s*(-?\d+(?:\.\d+)?)[^}]*resetInSec\s*:\s*(-?\d+(?:\.\d+)?)[^}]*\}`)
	reRollingRst  = regexp.MustCompile(`rollingUsage:\s*\$R\[\d+\]\s*=\s*\{[^}]*resetInSec\s*:\s*(-?\d+(?:\.\d+)?)[^}]*usagePercent\s*:\s*(-?\d+(?:\.\d+)?)[^}]*\}`)
	reWeeklyPct   = regexp.MustCompile(`weeklyUsage:\s*\$R\[\d+\]\s*=\s*\{[^}]*usagePercent\s*:\s*(-?\d+(?:\.\d+)?)[^}]*resetInSec\s*:\s*(-?\d+(?:\.\d+)?)[^}]*\}`)
	reWeeklyRst   = regexp.MustCompile(`weeklyUsage:\s*\$R\[\d+\]\s*=\s*\{[^}]*resetInSec\s*:\s*(-?\d+(?:\.\d+)?)[^}]*usagePercent\s*:\s*(-?\d+(?:\.\d+)?)[^}]*\}`)
	reMonthlyPct  = regexp.MustCompile(`monthlyUsage:\s*\$R\[\d+\]\s*=\s*\{[^}]*usagePercent\s*:\s*(-?\d+(?:\.\d+)?)[^}]*resetInSec\s*:\s*(-?\d+(?:\.\d+)?)[^}]*\}`)
	reMonthlyRst  = regexp.MustCompile(`monthlyUsage:\s*\$R\[\d+\]\s*=\s*\{[^}]*resetInSec\s*:\s*(-?\d+(?:\.\d+)?)[^}]*usagePercent\s*:\s*(-?\d+(?:\.\d+)?)[^}]*\}`)
)

// runtime per-account state
type opencodeAccountRuntime struct {
	Name        string
	Cookie      string
	WorkspaceID string
	Workspaces  []workspaceEntry
	Cache       quotaAccount
	CacheMu     sync.RWMutex
}

var (
	opencodeAccounts   []*opencodeAccountRuntime
	opencodeAcctMu     sync.RWMutex
	opencodeHTTPClient = &http.Client{Timeout: quotaHTTPTimeout, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	opencodeBgOnce     sync.Once
	opencodeBgStop     chan struct{}
)

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

func initOpenCodeAccounts(cfgs []openCodeGoAcctCfg) {
	opencodeAcctMu.Lock()
	defer opencodeAcctMu.Unlock()

	// Preserve existing in-memory accounts (from Dashboard). Only add
	// config entries that don't already exist, and never remove accounts.
	seen := make(map[string]bool)
	for _, a := range opencodeAccounts {
		seen[a.Name] = true
	}
	for _, cfg := range cfgs {
		name := strings.TrimSpace(cfg.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		cookie := strings.TrimSpace(cfg.AuthCookie)
		opencodeAccounts = append(opencodeAccounts, &opencodeAccountRuntime{
			Name:   name,
			Cookie: cookie,
			Cache: quotaAccount{
				Name:    name,
				Success: false,
				Windows: []quotaWindow{},
				Error:   "not fetched yet",
			},
		})
	}
	startQuotaRefreshLoop()
}

func startQuotaRefreshLoop() {
	opencodeBgOnce.Do(func() {
		opencodeBgStop = make(chan struct{})
		go quotaRefreshLoop()
	})
}

func stopQuotaRefreshLoop() {
	if opencodeBgStop != nil {
		close(opencodeBgStop)
		opencodeBgOnce = sync.Once{}
	}
}

func quotaRefreshLoop() {
	for {
		cfg := currentConfig()
		interval := cfg.RefreshSeconds
		if interval <= 0 {
			interval = 60 // fallback when bg is on but refresh_seconds=0
		}
		select {
		case <-opencodeBgStop:
			return
		case <-time.After(time.Duration(interval) * time.Second):
		}
		refreshAllQuotas()
	}
}

// ---------------------------------------------------------------------------
// HTTP + Parse
// ---------------------------------------------------------------------------

func buildCookieHeader(raw string) string {
	c := strings.TrimSpace(raw)
	if c == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(c), "cookie:") {
		c = strings.TrimSpace(c[7:])
	}
	for _, part := range strings.Split(c, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "auth=") {
			return part
		}
	}
	if strings.Contains(c, "=") {
		return c
	}
	return "auth=" + c
}

func parseWindow(pctRe, rstRe *regexp.Regexp, html string) (float64, int, bool) {
	if m := pctRe.FindStringSubmatch(html); m != nil {
		return parseFloat(m[1]), parseIntVal(m[2]), true
	}
	if m := rstRe.FindStringSubmatch(html); m != nil {
		return parseFloat(m[2]), parseIntVal(m[1]), true
	}
	return 0, 0, false
}

func parseFloat(s string) float64 {
	var f float64
	fmt.Sscanf(strings.TrimSpace(s), "%f", &f)
	return f
}

func parseIntVal(s string) int {
	var i int
	fmt.Sscanf(strings.TrimSpace(s), "%d", &i)
	return i
}

func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func resolveWorkspaces(cookie string) ([]workspaceEntry, error) {
	c := buildCookieHeader(cookie)
	if c == "" {
		return nil, fmt.Errorf("auth cookie is empty")
	}
	url := fmt.Sprintf("https://opencode.ai/_server?id=%s", opencodeServerID)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", c)
	req.Header.Set("X-Server-Id", opencodeServerID)
	req.Header.Set("X-Server-Instance", fmt.Sprintf("server-fn:%d", time.Now().UnixNano()))
	req.Header.Set("User-Agent", quotaUserAgent)
	req.Header.Set("Origin", "https://opencode.ai")
	req.Header.Set("Referer", "https://opencode.ai")
	req.Header.Set("Accept", "text/javascript, application/json;q=0.9, */*;q=0.8")

	resp, err := opencodeHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("workspace query failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, fmt.Errorf("auth cookie expired or invalid (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("workspace query returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxQuotaHTMLBytes))
	if err != nil {
		return nil, fmt.Errorf("read workspace response failed: %w", err)
	}

	matches := reWorkspaceEntry.FindAllStringSubmatch(string(body), -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("no workspace found for this account")
	}
	seen := make(map[string]bool)
	var entries []workspaceEntry
	for _, m := range matches {
		id := m[1]
		if seen[id] {
			continue
		}
		seen[id] = true
		entries = append(entries, workspaceEntry{ID: id, Name: m[2]})
	}
	return entries, nil
}

func fetchAndParseQuota(cookie, workspaceID string) ([]quotaWindow, error) {
	c := buildCookieHeader(cookie)
	if c == "" {
		return nil, fmt.Errorf("auth cookie is empty")
	}

	// Resolve workspaces if we don't have an explicit wrk_xxx
	if workspaceID == "" || !strings.HasPrefix(workspaceID, "wrk_") {
		entries, err := resolveWorkspaces(cookie)
		if err != nil {
			return nil, fmt.Errorf("workspace resolution failed: %w", err)
		}
		if len(entries) == 0 {
			return nil, fmt.Errorf("no workspace found")
		}
		workspaceID = entries[0].ID
	}

	// Fetch dashboard using resolved workspace ID
	dashboardURL := fmt.Sprintf("%s/%s/go", opencodeDashboardBase, workspaceID)
	req, err := http.NewRequest(http.MethodGet, dashboardURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", c)
	req.Header.Set("User-Agent", quotaUserAgent)
	req.Header.Set("Accept", "text/html, application/xhtml+xml")

	resp, err := opencodeHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, fmt.Errorf("auth cookie expired or invalid (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode == 302 || resp.StatusCode == 301 {
		return nil, fmt.Errorf("dashboard redirected (HTTP %d) - workspace ID may be wrong", resp.StatusCode)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("dashboard returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxQuotaHTMLBytes))
	if err != nil {
		return nil, fmt.Errorf("read body failed: %w", err)
	}
	html := string(body)

	windows := make([]quotaWindow, 0, 3)

	patterns := []struct {
		label  string
		pctRe  *regexp.Regexp
		rstRe  *regexp.Regexp
	}{
		{"5h Rolling", reRollingPct, reRollingRst},
		{"Weekly", reWeeklyPct, reWeeklyRst},
		{"Monthly", reMonthlyPct, reMonthlyRst},
	}

	for _, p := range patterns {
		if pct, rst, ok := parseWindow(p.pctRe, p.rstRe, html); ok {
			used := clampPercent(pct)
			windows = append(windows, quotaWindow{
				Label:      p.label,
				Used:       used,
				Remaining:  100.0 - used,
				Total:      100.0,
				Unit:       "%",
				ResetInSec: rst,
			})
		}
	}

	if len(windows) == 0 {
		return nil, fmt.Errorf("could not parse quota data from dashboard HTML")
	}
	return windows, nil
}

// ---------------------------------------------------------------------------
// Refresh logic
// ---------------------------------------------------------------------------

func refreshSingleQuota(acct *opencodeAccountRuntime) {
	// Refresh workspace list if empty
	if len(acct.Workspaces) == 0 && acct.Cookie != "" {
		if entries, err := resolveWorkspaces(acct.Cookie); err == nil {
			acct.Workspaces = entries
		}
	}
	windows, err := fetchAndParseQuota(acct.Cookie, acct.WorkspaceID)
	acct.CacheMu.Lock()
	defer acct.CacheMu.Unlock()
	acct.Cache.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		acct.Cache.Success = false
		acct.Cache.Windows = nil
		acct.Cache.Error = err.Error()
	} else {
		acct.Cache.Success = true
		acct.Cache.Windows = windows
		acct.Cache.Error = ""
	}
}

func refreshAllQuotas() {
	opencodeAcctMu.RLock()
	accounts := make([]*opencodeAccountRuntime, len(opencodeAccounts))
	copy(accounts, opencodeAccounts)
	opencodeAcctMu.RUnlock()

	var wg sync.WaitGroup
	for _, a := range accounts {
		if a.Cookie == "" {
			continue
		}
		wg.Add(1)
		go func(acct *opencodeAccountRuntime) {
			defer wg.Done()
			refreshSingleQuota(acct)
		}(a)
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func handleOpenCodeQuotaGet(query map[string][]string) pluginapi.ManagementResponse {
	// Check for query-param actions (for dashboard resource API which is GET-only)
	if action, ok := getQueryParam(query, "action"); ok && action != "" {
		acctName, _ := getQueryParam(query, "account")
		cookie, _ := getQueryParam(query, "cookie")
		workspace, _ := getQueryParam(query, "workspace")
		action = strings.ToLower(strings.TrimSpace(action))
		acctName = strings.TrimSpace(acctName)

		if action == "addaccount" {
			if acctName == "" {
				return jsonResponse(http.StatusBadRequest, map[string]string{"error": "account name is required"})
			}
			opencodeAcctMu.Lock()
			for _, a := range opencodeAccounts {
				if a.Name == acctName {
					opencodeAcctMu.Unlock()
					return jsonResponse(http.StatusConflict, map[string]string{"error": "account already exists"})
				}
			}
			r := &opencodeAccountRuntime{
				Name: acctName, Cookie: strings.TrimSpace(cookie),
				Cache: quotaAccount{Name: acctName, Success: false, Windows: []quotaWindow{}, Error: "not fetched yet"},
			}
			opencodeAccounts = append(opencodeAccounts, r)
			opencodeAcctMu.Unlock()
			if cookie != "" {
				refreshSingleQuota(r)
			}
		} else if action == "removeaccount" || action == "setcookie" || action == "setworkspace" || action == "refresh" {
			if acctName == "" {
				return jsonResponse(http.StatusBadRequest, map[string]string{"error": "account name is required"})
			}
			opencodeAcctMu.RLock()
			var target *opencodeAccountRuntime
			for _, a := range opencodeAccounts {
				if a.Name == acctName { target = a; break }
			}
			opencodeAcctMu.RUnlock()
			if target == nil {
				return jsonResponse(http.StatusNotFound, map[string]string{"error": "account not found"})
			}
			switch action {
			case "removeaccount":
				opencodeAcctMu.Lock()
				for i, a := range opencodeAccounts {
					if a.Name == acctName { opencodeAccounts = append(opencodeAccounts[:i], opencodeAccounts[i+1:]...); break }
				}
				opencodeAcctMu.Unlock()
			case "setcookie":
				c := strings.TrimSpace(cookie)
				if c == "" { return jsonResponse(http.StatusBadRequest, map[string]string{"error": "cookie is required"}) }
				target.Cookie = c
				if workspace != "" {
					target.WorkspaceID = workspace
				}
				refreshSingleQuota(target)
			case "setworkspace":
				if workspace == "" { return jsonResponse(http.StatusBadRequest, map[string]string{"error": "workspace is required"}) }
				target.WorkspaceID = workspace
				refreshSingleQuota(target)
			case "refresh":
				if target.Cookie == "" { return jsonResponse(http.StatusBadRequest, map[string]string{"error": "no cookie configured"}) }
				refreshSingleQuota(target)
			}
		} else {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "unknown action"})
		}
	}

	opencodeAcctMu.RLock()
	accounts := make([]quotaAccount, len(opencodeAccounts))
	for i, a := range opencodeAccounts {
		a.CacheMu.RLock()
		accounts[i] = a.Cache
		a.CacheMu.RUnlock()
		accounts[i].Name = a.Name
		accounts[i].WorkspaceID = a.WorkspaceID
		accounts[i].Workspaces = a.Workspaces
	}
	opencodeAcctMu.RUnlock()
	return jsonResponse(http.StatusOK, quotaResponse{Accounts: accounts})
}

func getQueryParam(query map[string][]string, key string) (string, bool) {
	if v, ok := query[key]; ok && len(v) > 0 {
		return v[0], true
	}
	return "", false
}

func handleOpenCodeQuotaPost(body []byte) pluginapi.ManagementResponse {
	var req struct {
		Action  string `json:"action"`
		Account string `json:"account"`
		Cookie  string `json:"cookie"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
	}
	acctName := strings.TrimSpace(req.Account)
	action := strings.ToLower(strings.TrimSpace(req.Action))

	// addaccount does not need an existing target
	if action == "addaccount" {
		cookie := strings.TrimSpace(req.Cookie)
		if acctName == "" {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "account name is required"})
		}
		opencodeAcctMu.Lock()
		// check duplicate
		for _, a := range opencodeAccounts {
			if a.Name == acctName {
				opencodeAcctMu.Unlock()
				return jsonResponse(http.StatusConflict, map[string]string{"error": "account already exists"})
			}
		}
		r := &opencodeAccountRuntime{
			Name:   acctName,
			Cookie: cookie,
			Cache: quotaAccount{
				Name:    acctName,
				Success: false,
				Windows: []quotaWindow{},
				Error:   "not fetched yet",
			},
		}
		opencodeAccounts = append(opencodeAccounts, r)
		opencodeAcctMu.Unlock()
		if cookie != "" {
			refreshSingleQuota(r)
		}
		return handleOpenCodeQuotaGet(nil)
	}

	if acctName == "" {
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "account name is required"})
	}

	opencodeAcctMu.RLock()
	var target *opencodeAccountRuntime
	for _, a := range opencodeAccounts {
		if a.Name == acctName {
			target = a
			break
		}
	}
	opencodeAcctMu.RUnlock()

	if target == nil {
		return jsonResponse(http.StatusNotFound, map[string]string{"error": "account not found"})
	}

	switch strings.ToLower(action) {
	case "removeaccount":
		opencodeAcctMu.Lock()
		for i, a := range opencodeAccounts {
			if a.Name == acctName {
				opencodeAccounts = append(opencodeAccounts[:i], opencodeAccounts[i+1:]...)
				break
			}
		}
		opencodeAcctMu.Unlock()
		return handleOpenCodeQuotaGet(nil)
	case "setcookie":
		cookie := strings.TrimSpace(req.Cookie)
		if cookie == "" {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "cookie is required"})
		}
		target.Cookie = cookie
		refreshSingleQuota(target)
		return handleOpenCodeQuotaGet(nil)
	case "refresh":
		if target.Cookie == "" {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "no cookie configured for this account"})
		}
		refreshSingleQuota(target)
		return handleOpenCodeQuotaGet(nil)
	default:
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "unknown action, use 'setcookie', 'refresh', 'addaccount', or 'removeaccount'"})
	}
}
