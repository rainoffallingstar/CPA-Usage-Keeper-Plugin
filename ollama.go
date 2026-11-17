package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	// Upper bound on the extracted Cloud usage region; the real region is a
	// few KB, so this only guards against a pathological page.
	ollamaMaxCloudUsageBlockBytes = 256 << 10 // 256 KB
	ollamaLabelSession            = "Session"
	ollamaLabelWeekly             = "Weekly"
)

var (
	// ollama.com re-skinned /settings in 2026-10: the section heading moved
	// from <span>Cloud usage</span> to <h2 class="...">Cloud usage</h2> and the
	// old "</div><script>" terminator drifted far past the section. Match the
	// heading tag-agnostically and end the region at the "notify me" form that
	// directly follows the usage meters.
	reOllamaCloudUsageHeading = regexp.MustCompile(`(?is)<(?:h[1-6]|span|div|p)\b[^>]*>\s*Cloud usage\s*</(?:h[1-6]|span|div|p)>`)
	reOllamaCloudUsageEndHx   = regexp.MustCompile(`(?is)<form\b[^>]*hx-post="/settings"`)
	reOllamaCloudUsageEndForm = regexp.MustCompile(`(?is)<form\b[^>]*action="/settings"`)

	reOllamaPlan         = regexp.MustCompile(`(?is)rounded-full[^>]*capitalize[^>]*>\s*([^<]+?)\s*</span`)
	reOllamaPctUsed      = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*%\s*used`)
	reOllamaUsageTrack   = regexp.MustCompile(`(?is)data-usage-track[^>]*aria-label="([^"]+)"[^>]*>(.*?)</div>`)
	reOllamaUsageSegment = regexp.MustCompile(`(?is)<button\b[^>]*data-usage-segment[^>]*>`)
	reOllamaModel        = regexp.MustCompile(`data-model="([^"]+)"`)
	reOllamaRequests     = regexp.MustCompile(`data-requests="(\d+)"`)
	reOllamaWidth        = regexp.MustCompile(`width:\s*([\d.]+)%`)
	reOllamaPeriodHeader = regexp.MustCompile(`(?s)<div class="flex justify-between mb-2">(.*?)</div>`)
	reOllamaHeaderSpan   = regexp.MustCompile(`(?s)<span class="text-sm[^"]*"[^>]*>\s*([^<]+?)\s*</span`)
	reOllamaResetInfo    = regexp.MustCompile(`(?is)class="[^"]*local-time[^"]*"[^>]*data-time="([^"]+)"[^>]*>\s*([^<]+?)\s*</div>`)
	reOllamaNotLoggedIn  = regexp.MustCompile(`(?i)(sign in|log in|invalid credentials)`)
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
		cookie := strings.TrimSpace(cfg.SessionCookie)
		if normalized := buildOllamaCookieHeader(cookie); normalized != "" {
			cookie = normalized
		}
		ollamaAccounts = append(ollamaAccounts, &ollamaAccountRuntime{
			Name:          name,
			SessionCookie: cookie,
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

// ollamaCookieCleaner rewrites the punctuation that clipboard round-trips
// silently substitute for ASCII. A Chinese-locale browser — or a chat app used
// to relay the value — turns ";" into the full-width "；" (U+FF1B). ollama.com
// splits the Cookie header on ASCII ";" only, so a cookie pasted as
// "aid=…；__Secure-session=…" is sent as a single aid cookie: the session
// cookie never arrives, the settings page renders logged-out, and we wrongly
// blame the user's cookie for being invalid.
var ollamaCookieCleaner = strings.NewReplacer(
	"\uFF1B", ";", "\uFF1D", "=", "\uFF0C", ",", "\uFF1A", ":",
	"\u3000", " ",
	"\u201C", "\"", "\u201D", "\"", "\u2018", "'", "\u2019", "'",
	"\u200B", "", "\u200C", "", "\u200D", "", "\uFEFF", "",
	"\r", " ", "\n", " ", "\t", " ",
)

// reOllamaMarkdownEscape strips the backslash that Markdown-ish surfaces (chat
// apps, issue trackers, note apps) insert in front of punctuation. Pasting
// "aid=<uuid>; __Secure-session=<token>" through such a surface yields
// "aid=<uuid>,\_\_Secure-session=<token>": the cookie name itself is escaped,
// so ollama.com never recognises the session cookie. Cookie values are base64url
// and never contain a literal backslash, so unescaping is lossless here.
var reOllamaMarkdownEscape = regexp.MustCompile(`\\([_;,=."'\\*#+\-!()\[\]{}+&|%$@:<>?/~^])`)

// ollamaCookieSeparatorNames are the "name=" prefixes that can follow a
// separator. Used by splitOllamaCookiePairs to tell a mis-typed separator apart
// from a comma that legitimately appears inside a value.
var ollamaCookieSeparatorNames = []string{
	"__secure-session=", "__host-session=", "aid=", "session=", "ollama_session=",
}

// splitOllamaCookiePairs cuts raw on ASCII ";" and then on a comma that is
// immediately followed by a known cookie name. Some relay paths turn ";" into
// "," (a Markdown/JSON-ish re-serialisation); without the second cut the whole
// string collapses into one aid cookie and the session cookie is never sent.
// A comma that is not followed by a known name is left untouched, so opaque
// values (base64url, JWT) survive intact.
func splitOllamaCookiePairs(raw string) []string {
	out := make([]string, 0, 4)
	for _, part := range strings.Split(raw, ";") {
		start := 0
		lower := strings.ToLower(part)
		for i := 0; i < len(part); i++ {
			if part[i] != ',' {
				continue
			}
			rest := strings.TrimLeft(lower[i+1:], " \t")
			matched := false
			for _, name := range ollamaCookieSeparatorNames {
				if strings.HasPrefix(rest, name) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
			if piece := strings.TrimSpace(part[start:i]); piece != "" {
				out = append(out, piece)
			}
			start = i + 1
			i = start - 1
		}
		if piece := strings.TrimSpace(part[start:]); piece != "" {
			out = append(out, piece)
		}
	}
	return out
}

// normalizeOllamaCookie turns any accepted user-supplied shape — a full
// "Cookie: ..." header, a "name=value; name2=value2" string, or a bare session
// value — into a canonical Cookie header value, repairing full-width
// punctuation and stray separators along the way.
func normalizeOllamaCookie(raw string) string {
	cookie := strings.TrimSpace(raw)
	if cookie == "" {
		return ""
	}
	cookie = ollamaCookieCleaner.Replace(cookie)
	cookie = reOllamaMarkdownEscape.ReplaceAllString(cookie, "$1")
	if strings.HasPrefix(strings.ToLower(cookie), "cookie:") {
		cookie = strings.TrimSpace(cookie[len("cookie:"):])
	}
	// Re-assemble clean "name=value" pairs; this drops duplicated separator
	// runs, empty pairs and trailing semicolons left behind by copy-paste.
	// Commas are also honoured when a known cookie name follows them: a relay
	// that rewrote ";" as "," would otherwise leave __Secure-session glued to
	// the aid value, and the server would see a single, unusable aid cookie.
	pairs := splitOllamaCookiePairs(cookie)
	if len(pairs) == 0 {
		return ""
	}
	joined := strings.Join(pairs, "; ")
	if !looksLikeOllamaCookiePairs(joined) {
		// A bare session value carries no usable "name=" prefix. This cannot be
		// decided with a plain "contains '='" test: the real __Secure-session
		// token is base64 and ends in "==", so that test would treat ~460
		// characters of base64 as the cookie *name* and send a header the
		// server can never match.
		return "__Secure-session=" + joined
	}
	return joined
}

// ollamaCookieNameOK matches an RFC 6265 cookie-name token.
var ollamaCookieNameOK = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")

// looksLikeOllamaCookiePairs reports whether s is a "name=value[; name=value]"
// list rather than a single opaque session token.
func looksLikeOllamaCookiePairs(s string) bool {
	if strings.Contains(s, ";") {
		return true
	}
	name, _, ok := strings.Cut(s, "=")
	// A real cookie name is short and made of token characters; a base64 blob
	// is long, and by the time it reaches '=' it is already hundreds of bytes in.
	return ok && name != "" && len(name) <= 32 && ollamaCookieNameOK.MatchString(name)
}

// buildOllamaCookieHeader normalizes the session cookie. It accepts either a
// full "aid=...; __Secure-session=..." string, a "Cookie: ..." header, or a
// bare session value (in which case it is wrapped as __Secure-session=...).
func buildOllamaCookieHeader(sessionCookie string) string {
	return normalizeOllamaCookie(sessionCookie)
}

// ollamaRedirectsToSignIn reports whether target belongs to ollama.com's login
// flow. /settings answers an unauthenticated request with 303 → /signin, and
// /signin bounces through api.workos.com to signin.ollama.com.
func ollamaRedirectsToSignIn(target string) bool {
	u, err := url.Parse(target)
	if err != nil {
		return false
	}
	path := strings.ToLower(u.Path)
	host := strings.ToLower(u.Hostname())
	return strings.Contains(path, "signin") || strings.Contains(path, "sign-in") ||
		strings.HasPrefix(host, "signin.") || strings.Contains(host, "workos.com")
}

// ollamaSessionCookieNames are the cookie names ollama.com accepts as the login
// session. Which one is present has changed over time, so all are honoured.
var ollamaSessionCookieNames = []string{
	"__secure-session", "__host-session", "session", "ollama_session",
}

// ollamaCookieHasSession reports whether a normalized Cookie header actually
// carries a non-empty session cookie. ollama.com also sets "aid" (an anonymous
// visitor id), so a header with only "aid" is syntactically valid yet always
// renders the sign-in page — checking up front turns a misleading
// "未登录或 cookie 无效" into an actionable "cookie 里缺少会话字段".
func ollamaCookieHasSession(header string) bool {
	for _, part := range strings.Split(header, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		name = strings.ToLower(strings.TrimSpace(name))
		for _, want := range ollamaSessionCookieNames {
			if name == want {
				return true
			}
		}
	}
	return false
}

// ollamaCookieNameList returns just the cookie *names* in a header, for error
// messages. Values are never logged or surfaced: the session token is a bearer
// credential for the user's whole Ollama account.
func ollamaCookieNameList(header string) string {
	names := make([]string, 0, 3)
	for _, part := range strings.Split(header, ";") {
		if name, _, ok := strings.Cut(strings.TrimSpace(part), "="); ok && name != "" {
			names = append(names, strings.TrimSpace(name))
		}
	}
	if len(names) == 0 {
		return "（空）"
	}
	return strings.Join(names, ", ")
}

// extractOllamaCloudUsageBlock returns the markup between the "Cloud usage"
// heading and the "notify me" form that follows it. Both ends are matched
// loosely on purpose: the previous exact-match extraction broke silently the
// moment ollama.com re-skinned the page.
func extractOllamaCloudUsageBlock(html string) (string, error) {
	loc := reOllamaCloudUsageHeading.FindStringIndex(html)
	if loc == nil {
		return "", fmt.Errorf("页面中未找到 Cloud usage 区块（可能未登录或页面结构已变更）")
	}
	rest := html[loc[1]:]

	end := len(rest)
	for _, re := range []*regexp.Regexp{reOllamaCloudUsageEndHx, reOllamaCloudUsageEndForm} {
		if m := re.FindStringIndex(rest); m != nil && m[0] < end {
			end = m[0]
		}
	}
	if end > ollamaMaxCloudUsageBlockBytes {
		end = ollamaMaxCloudUsageBlockBytes
	}

	block := rest[:end]
	if strings.TrimSpace(block) == "" {
		return "", fmt.Errorf("Cloud usage 区块为空（可能未登录或页面结构已变更）")
	}
	return block, nil
}

// parseOllamaPlan reads the plan badge (e.g. "pro") off the settings page. The
// badge renders inside the "Usage credits" card, which sits *above* the Cloud
// usage heading, so this must be fed the whole document and not the extracted
// usage block.
func parseOllamaPlan(page string) string {
	m := reOllamaPlan.FindStringSubmatch(page)
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
	plan := parseOllamaPlan(html)
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
	if !ollamaCookieHasSession(cookieHeader) {
		return nil, fmt.Errorf("cookie 里只有 %s，缺少会话字段（需要 __Secure-session）；"+
			"aid 只是匿名访客 ID，无法登录", ollamaCookieNameList(cookieHeader))
	}

	req, err := http.NewRequest(http.MethodGet, ollamaSettingsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", cookieHeader)
	req.Header.Set("User-Agent", ollamaUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	// ollama.com answers an unauthenticated /settings with 303 → /signin, and
	// /signin bounces through api.workos.com before landing on the sign-in page.
	// Following that chain costs two extra round trips and only tells us what we
	// already know, so stop at the first redirect toward the login flow.
	redirectedToSignIn := false
	client := *ollamaHTTPClient
	client.CheckRedirect = func(r *http.Request, _ []*http.Request) error {
		if ollamaRedirectsToSignIn(r.URL.String()) {
			redirectedToSignIn = true
			return http.ErrUseLastResponse
		}
		return nil
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if redirectedToSignIn {
		return nil, fmt.Errorf("未登录或 cookie 已过期：ollama.com 把 /settings 重定向到了登录页"+
			"（已发送的 cookie: %s）。请重新登录后复制完整 Cookie", ollamaCookieNameList(cookieHeader))
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("认证失败 (HTTP %d)，请检查 session cookie", resp.StatusCode)
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, fmt.Errorf("settings 页面被重定向 (HTTP %d → %s)，无法判断登录状态",
			resp.StatusCode, resp.Header.Get("Location"))
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
			storedCookie := strings.TrimSpace(cookie)
			if normalized := buildOllamaCookieHeader(storedCookie); normalized != "" {
				storedCookie = normalized
			}
			r := &ollamaAccountRuntime{
				Name:          acctName,
				SessionCookie: storedCookie,
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
			if normalized := buildOllamaCookieHeader(c); normalized != "" {
				c = normalized
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
			key := refreshKey("ollama", acctName)
			if !beginRefresh(key) {
				return jsonResponse(http.StatusOK, map[string]any{"refreshing": true, "account": acctName, "started": false})
			}
			go func() {
				defer endRefresh(key)
				refreshOllamaQuota(target)
			}()
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
		acct.Refreshing = refreshRunning("ollama", a.Name)
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
		if normalized := buildOllamaCookieHeader(cookie); normalized != "" {
			cookie = normalized
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
		if normalized := buildOllamaCookieHeader(cookie); normalized != "" {
			cookie = normalized
		}
		target.SessionCookie = cookie
		if req.ShowSession != nil {
			target.ShowSession = *req.ShowSession
		}
		if req.ShowWeekly != nil {
			target.ShowWeekly = *req.ShowWeekly
		}
		persistOllamaAccount(target.Name, target.SessionCookie, target.ShowSession, target.ShowWeekly)
		key := refreshKey("ollama", acctName)
		if !beginRefresh(key) {
			return jsonResponse(http.StatusOK, map[string]any{"refreshing": true, "account": acctName, "started": false})
		}
		go func() {
			defer endRefresh(key)
			refreshOllamaQuota(target)
		}()
		return handleOllamaQuotaGet(nil)
	case "refresh":
		if target.SessionCookie == "" {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "no session cookie configured for this account"})
		}
		key := refreshKey("ollama", acctName)
		if !beginRefresh(key) {
			return jsonResponse(http.StatusOK, map[string]any{"refreshing": true, "account": acctName, "started": false})
		}
		go func() {
			defer endRefresh(key)
			refreshOllamaQuota(target)
		}()
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
		// Repair cookies that a previous build (or a copy-paste through a
		// Markdown-ish surface) stored in a form ollama.com cannot parse — e.g.
		// "aid=<uuid>,\_\_Secure-session=<token>". Without this, an otherwise
		// valid login keeps failing until the user re-pastes the cookie.
		stored := cookie
		if normalized := buildOllamaCookieHeader(cookie); normalized != "" {
			cookie = normalized
			if normalized != stored {
				go persistOllamaAccount(name, normalized, showSession != 0, showWeekly != 0)
			}
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
	// Store the canonical form so a later load never has to re-derive it, and
	// so a repaired cookie stays repaired across restarts.
	if normalized := buildOllamaCookieHeader(cookie); normalized != "" {
		cookie = normalized
	}
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
