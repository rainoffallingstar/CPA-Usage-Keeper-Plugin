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
	quotaUserAgent        = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Gecko/20100101 Firefox/148.0"
	quotaHTTPTimeout      = 10 * time.Second
	maxQuotaHTMLBytes     = 4 << 20 // 4 MB
)

var (
	reRollingPct  = regexp.MustCompile(`rollingUsage:\s*\$R\[\d+\]\s*=\s*\{[^}]*usagePercent\s*:\s*(-?\d+(?:\.\d+)?)[^}]*resetInSec\s*:\s*(-?\d+(?:\.\d+)?)[^}]*\}`)
	reRollingRst  = regexp.MustCompile(`rollingUsage:\s*\$R\[\d+\]\s*=\s*\{[^}]*resetInSec\s*:\s*(-?\d+(?:\.\d+)?)[^}]*usagePercent\s*:\s*(-?\d+(?:\.\d+)?)[^}]*\}`)
	reWeeklyPct   = regexp.MustCompile(`weeklyUsage:\s*\$R\[\d+\]\s*=\s*\{[^}]*usagePercent\s*:\s*(-?\d+(?:\.\d+)?)[^}]*resetInSec\s*:\s*(-?\d+(?:\.\d+)?)[^}]*\}`)
	reWeeklyRst   = regexp.MustCompile(`weeklyUsage:\s*\$R\[\d+\]\s*=\s*\{[^}]*resetInSec\s*:\s*(-?\d+(?:\.\d+)?)[^}]*usagePercent\s*:\s*(-?\d+(?:\.\d+)?)[^}]*\}`)
	reMonthlyPct  = regexp.MustCompile(`monthlyUsage:\s*\$R\[\d+\]\s*=\s*\{[^}]*usagePercent\s*:\s*(-?\d+(?:\.\d+)?)[^}]*resetInSec\s*:\s*(-?\d+(?:\.\d+)?)[^}]*\}`)
	reMonthlyRst  = regexp.MustCompile(`monthlyUsage:\s*\$R\[\d+\]\s*=\s*\{[^}]*resetInSec\s*:\s*(-?\d+(?:\.\d+)?)[^}]*usagePercent\s*:\s*(-?\d+(?:\.\d+)?)[^}]*\}`)
)

// runtime per-account state
type opencodeAccountRuntime struct {
	Name      string
	Cookie    string
	Cache     quotaAccount
	CacheMu   sync.RWMutex
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

	// preserve existing cookies if account names match
	oldByName := make(map[string]string)
	for _, a := range opencodeAccounts {
		oldByName[a.Name] = a.Cookie
	}

	opencodeAccounts = make([]*opencodeAccountRuntime, 0, len(cfgs))
	for _, cfg := range cfgs {
		name := strings.TrimSpace(cfg.Name)
		if name == "" {
			continue
		}
		cookie := strings.TrimSpace(cfg.AuthCookie)
		// prefer existing in-memory cookie over config cookie
		if existing, ok := oldByName[name]; ok && existing != "" {
			cookie = existing
		}
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

func fetchAndParseQuota(cookie string) ([]quotaWindow, error) {
	c := buildCookieHeader(cookie)
	if c == "" {
		return nil, fmt.Errorf("auth cookie is empty")
	}

	// Build workspace dashboard URL (using Default workspace)
	url := opencodeDashboardBase + "/Default/go"
	req, err := http.NewRequest(http.MethodGet, url, nil)
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
		return nil, fmt.Errorf("dashboard redirected (HTTP %d) - cookie may be expired", resp.StatusCode)
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
	windows, err := fetchAndParseQuota(acct.Cookie)
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

func handleOpenCodeQuotaGet() pluginapi.ManagementResponse {
	opencodeAcctMu.RLock()
	accounts := make([]quotaAccount, len(opencodeAccounts))
	for i, a := range opencodeAccounts {
		a.CacheMu.RLock()
		accounts[i] = a.Cache
		a.CacheMu.RUnlock()
		accounts[i].Name = a.Name
	}
	opencodeAcctMu.RUnlock()
	return jsonResponse(http.StatusOK, quotaResponse{Accounts: accounts})
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

	switch strings.ToLower(req.Action) {
	case "setcookie":
		cookie := strings.TrimSpace(req.Cookie)
		if cookie == "" {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "cookie is required"})
		}
		target.Cookie = cookie
		refreshSingleQuota(target)
		return handleOpenCodeQuotaGet()
	case "refresh":
		if target.Cookie == "" {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "no cookie configured for this account"})
		}
		refreshSingleQuota(target)
		return handleOpenCodeQuotaGet()
	default:
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "unknown action, use 'setcookie' or 'refresh'"})
	}
}
