package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// ---------------------------------------------------------------------------
// Google Colab subscription / quota monitoring
//
// Auth flow mirrors googlecolab/colab-vscode:
//  1. login: plugin generates PKCE (code_verifier + S256 challenge) and a
//     nonce, starts a loopback HTTP server on 127.0.0.1:<port>, and returns
//     the Google authorization URL (redirect_uri = loopback address).
//  2. callback: user authorizes in the browser → Google redirects to the
//     loopback server with ?code=...&state=nonce=<id> → the code is captured.
//  3. exchange: plugin exchanges code + code_verifier + client_secret for
//     tokens via oauth2.googleapis.com/token; refresh_token is stored
//     (obfuscated) in SQLite.
//  4. quota: GET colab.pa.googleapis.com/v1/user-info?get_ccu_consumption_info=true
//     with a Bearer access_token (JIT refreshed from refresh_token).
// ---------------------------------------------------------------------------

var (
	colabOAuthClientID     = unmaskSecret([]byte{66, 67, 66, 71, 66, 69, 67, 71, 74, 67, 66, 70, 74, 94, 16, 5, 28, 7, 64, 17, 22, 18, 68, 7, 20, 24, 3, 68, 65, 18, 71, 30, 65, 74, 27, 65, 67, 23, 74, 23, 23, 28, 69, 17, 29, 22, 93, 18, 3, 3, 0, 93, 20, 28, 28, 20, 31, 22, 6, 0, 22, 1, 16, 28, 29, 7, 22, 29, 7, 93, 16, 28, 30}, 0x73)
	colabOAuthClientSecret = unmaskSecret([]byte{52, 60, 48, 32, 35, 43, 94, 54, 53, 71, 53, 26, 1, 17, 37, 34, 16, 63, 1, 55, 33, 5, 4, 25, 16, 3, 55, 43, 38, 94, 67, 26, 38, 2, 71}, 0x73)
)

func unmaskSecret(data []byte, key byte) string {
	res := make([]byte, len(data))
	for i, b := range data {
		res[i] = b ^ key
	}
	return string(res)
}

const (
	colabAuthEndpoint      = "https://accounts.google.com/o/oauth2/v2/auth"
	colabTokenEndpoint     = "https://oauth2.googleapis.com/token"
	colabUserInfoEndpoint  = "https://www.googleapis.com/oauth2/v2/userinfo"
	colabGapiDomain        = "https://colab.pa.googleapis.com"
	colabHTTPTimeout       = 20 * time.Second
	colabLoginTimeoutSec   = 300 // 5 minutes to complete browser authorization
	colabAccessTokenTtl    = 20 * time.Minute
)

// REQUIRED_SCOPES matches colab-vscode's minimum required scope set.
var colabRequiredScopes = []string{
	"profile",
	"email",
	"https://www.googleapis.com/auth/colaboratory",
}

// credEncryptKey is a lightweight obfuscation salt for stored refresh tokens.
// It is NOT strong crypto — it only prevents casual plaintext dump of tokens
// in the SQLite file. An attacker with database access can derive the key.
const credEncryptKey = "usage-keeper::colab::v1"

type colabAccountRuntime struct {
	Name         string
	RefreshToken string
	AccessToken  string
	TokenExpiry  time.Time
	Email        string
	Cache        quotaAccount
	CacheMu      sync.RWMutex
}

var (
	colabAccounts   []*colabAccountRuntime
	colabAcctMu     sync.RWMutex
	colabHTTPClient = &http.Client{Timeout: colabHTTPTimeout}
	colabBgOnce     sync.Once
	colabBgStop     chan struct{}

	// Loopback login state
	colabLoginMu    sync.Mutex
	colabLoginState = make(map[string]*colabLoginPending)
)

type colabLoginPending struct {
	Nonce         string
	CodeVerifier  string
	RedirectURI   string
	Account       string
	Code          string
	CodeReceived  bool
	StartedAt     time.Time
	Server        *http.Server
	ServerDone    chan struct{}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

func initColabAccounts() {
	colabBgOnce.Do(func() {
		colabBgStop = make(chan struct{})
		go colabQuotaRefreshLoop()
	})
	// Accounts are loaded from DB via loadColabAccountsFromDB (lazyInit).
}

func colabQuotaRefreshLoop() {
	for {
		select {
		case <-colabBgStop:
			return
		case <-time.After(5 * time.Minute):
		}
		colabAcctMu.RLock()
		tasks := make([]*colabAccountRuntime, 0)
		for _, a := range colabAccounts {
			if a.RefreshToken != "" {
				tasks = append(tasks, a)
			}
		}
		colabAcctMu.RUnlock()
		var wg sync.WaitGroup
		for _, a := range tasks {
			wg.Add(1)
			go func(acct *colabAccountRuntime) {
				defer wg.Done()
				refreshColabQuota(acct)
			}(a)
		}
		wg.Wait()
	}
}

// ---------------------------------------------------------------------------
// Credential obfuscation (lightweight)
// ---------------------------------------------------------------------------

func obfuscateToken(token string) string {
	if token == "" {
		return ""
	}
	key := sha256.Sum256([]byte(credEncryptKey))
	src := []byte(token)
	for i := range src {
		src[i] ^= key[i%len(key)]
	}
	return base64.StdEncoding.EncodeToString(src)
}

func deobfuscateToken(cipher string) string {
	if cipher == "" {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(cipher)
	if err != nil {
		return ""
	}
	key := sha256.Sum256([]byte(credEncryptKey))
	for i := range raw {
		raw[i] ^= key[i%len(key)]
	}
	return string(raw)
}

// ---------------------------------------------------------------------------
// DB persistence
// ---------------------------------------------------------------------------

func persistColabAccount(name, refreshToken, email string) {
	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil {
		return
	}
	d.Exec(`INSERT INTO colab_quota_accounts (name, refresh_token, email) VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET refresh_token=excluded.refresh_token, email=excluded.email`,
		name, obfuscateToken(refreshToken), email)
}

func deleteColabAccountFromDB(name string) {
	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil {
		return
	}
	d.Exec("DELETE FROM colab_quota_accounts WHERE name = ?", name)
}

func loadColabAccountsFromDB() {
	defer func() { recover() }()
	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil {
		return
	}
	rows, _ := d.Query("SELECT name, refresh_token, email FROM colab_quota_accounts")
	if rows == nil {
		return
	}
	defer rows.Close()
	colabAcctMu.Lock()
	defer colabAcctMu.Unlock()
	for rows.Next() {
		var name, tok, email string
		if rows.Scan(&name, &tok, &email) != nil {
			continue
		}
		seen := false
		for _, a := range colabAccounts {
			if a.Name == name {
				seen = true
				break
			}
		}
		if seen {
			continue
		}
		rt := deobfuscateToken(tok)
		colabAccounts = append(colabAccounts, &colabAccountRuntime{
			Name:         name,
			RefreshToken: rt,
			Email:        email,
			Cache: quotaAccount{
				Type:    "colab",
				Name:    name,
				Success: false,
				Windows: []quotaWindow{},
				Error:   "not fetched yet",
			},
		})
		if rt != "" {
			go refreshColabQuota(colabAccounts[len(colabAccounts)-1])
		}
	}
}

// ---------------------------------------------------------------------------
// PKCE helpers
// ---------------------------------------------------------------------------

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Fallback: use nanosecond timestamps (weak but functional).
		ts := time.Now().UnixNano()
		for i := range b {
			b[i] = byte(ts >> (i % 8 * 8))
		}
	}
	return b
}

func generateCodeVerifier() string {
	return base64.RawURLEncoding.EncodeToString(randomBytes(32))
}

func generateNonce() string {
	return hex.EncodeToString(randomBytes(16))
}

func generateCodeChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// ---------------------------------------------------------------------------
// Token operations
// ---------------------------------------------------------------------------

type colabTokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func colabExchangeCode(code, codeVerifier, redirectURI string) (*colabTokenResponse, error) {
	form := url.Values{}
	form.Set("client_id", colabOAuthClientID)
	form.Set("client_secret", colabOAuthClientSecret)
	form.Set("code", code)
	form.Set("code_verifier", codeVerifier)
	form.Set("grant_type", "authorization_code")
	if redirectURI != "" {
		form.Set("redirect_uri", redirectURI)
	}
	req, _ := http.NewRequest("POST", colabTokenEndpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := colabHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tr colabTokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("token response parse failed: %w", err)
	}
	if resp.StatusCode != 200 || tr.AccessToken == "" {
		msg := tr.ErrorDesc
		if msg == "" {
			msg = tr.Error
		}
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return nil, fmt.Errorf("token exchange failed: %s", msg)
	}
	return &tr, nil
}

// colabRefreshAccessToken exchanges a refresh_token for a fresh access_token.
func colabRefreshAccessToken(refreshToken string) (*colabTokenResponse, error) {
	form := url.Values{}
	form.Set("client_id", colabOAuthClientID)
	form.Set("client_secret", colabOAuthClientSecret)
	form.Set("refresh_token", refreshToken)
	form.Set("grant_type", "refresh_token")
	req, _ := http.NewRequest("POST", colabTokenEndpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := colabHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tr colabTokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("refresh response parse failed: %w", err)
	}
	if resp.StatusCode != 200 || tr.AccessToken == "" {
		msg := tr.ErrorDesc
		if msg == "" {
			msg = tr.Error
		}
		return nil, fmt.Errorf("token refresh failed: %s", msg)
	}
	return &tr, nil
}

// ensureFreshAccessToken JIT-refreshes the access token if it is near expiry.
func ensureFreshAccessToken(acct *colabAccountRuntime) error {
	if acct.RefreshToken == "" {
		return fmt.Errorf("no refresh token configured")
	}
	if acct.AccessToken != "" && time.Until(acct.TokenExpiry) > 2*time.Minute {
		return nil
	}
	resp, err := colabRefreshAccessToken(acct.RefreshToken)
	if err != nil {
		return err
	}
	acct.AccessToken = resp.AccessToken
	ttl := resp.ExpiresIn
	if ttl <= 0 {
		ttl = 3600
	}
	acct.TokenExpiry = time.Now().Add(time.Duration(ttl) * time.Second)
	if resp.RefreshToken != "" {
		acct.RefreshToken = resp.RefreshToken
		persistColabAccount(acct.Name, resp.RefreshToken, acct.Email)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Quota fetch
// ---------------------------------------------------------------------------

type colabUserInfo struct {
	SubscriptionTier        string `json:"subscriptionTier"`
	PaidComputeUnitsBalance *float64 `json:"paidComputeUnitsBalance"`
	ConsumptionRateHourly   float64 `json:"consumptionRateHourly"`
	AssignmentsCount        float64 `json:"assignmentsCount"`
	FreeCcuQuotaInfo        *struct {
		RemainingTokens         string  `json:"remainingTokens"`
		NextRefillTimestampSec  float64 `json:"nextRefillTimestampSec"`
	} `json:"freeCcuQuotaInfo"`
	EligibleAccelerators    []struct {
		Variant string   `json:"variant"`
		Models  []string `json:"models"`
	} `json:"eligibleAccelerators"`
}

func fetchColabUserInfo(accessToken string) (*colabUserInfo, error) {
	u, _ := url.Parse(colabGapiDomain)
	u.Path = "/v1/user-info"
	q := u.Query()
	q.Set("get_ccu_consumption_info", "true")
	u.RawQuery = q.Encode()

	req, _ := http.NewRequest("GET", u.String(), nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-Colab-Client-Agent", "vscode")
	resp, err := colabHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("colab user-info request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode == 401 {
		return nil, fmt.Errorf("unauthorized: token expired or revoked (HTTP 401)")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("colab user-info returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var ui colabUserInfo
	if err := json.Unmarshal(body, &ui); err != nil {
		return nil, fmt.Errorf("colab user-info parse failed: %w", err)
	}
	return &ui, nil
}

func refreshColabQuota(acct *colabAccountRuntime) {
	acct.CacheMu.Lock()
	defer acct.CacheMu.Unlock()
	acct.Cache.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	if acct.RefreshToken == "" {
		acct.Cache.Success = false
		acct.Cache.Error = "no credentials configured"
		return
	}
	if err := ensureFreshAccessToken(acct); err != nil {
		acct.Cache.Success = false
		acct.Cache.Error = err.Error()
		return
	}
	ui, err := fetchColabUserInfo(acct.AccessToken)
	if err != nil {
		acct.Cache.Success = false
		acct.Cache.Error = err.Error()
		return
	}

	acct.Cache.Success = true
	acct.Cache.Error = ""
	acct.Cache.Plan = colabTierLabel(ui.SubscriptionTier)

	windows := make([]quotaWindow, 0, 2)

	// Paid CCU balance (always shown when present)
	paidBalance := ui.PaidComputeUnitsBalance
	if paidBalance != nil {
		tot, usd, hint := computeColabCeiling(ui.SubscriptionTier, *paidBalance)
		w := quotaWindow{
			Label:     "付费 CCU 算力",
			Total:     tot,
			Remaining: *paidBalance,
			Used:      usd,
			Unit:      "CCU",
		}
		statusParts := make([]string, 0, 2)
		if ui.ConsumptionRateHourly > 0 {
			statusParts = append(statusParts, fmt.Sprintf("消耗率 %.2f CCU/h · %d 个运行实例", ui.ConsumptionRateHourly, int(ui.AssignmentsCount)))
		}
		if hint != "" {
			statusParts = append(statusParts, hint)
		}
		if len(statusParts) > 0 {
			w.StatusText = strings.Join(statusParts, " · ")
		}
		windows = append(windows, w)
	}

	// Free CCU quota (when paid balance is not remaining)
	if ui.FreeCcuQuotaInfo != nil {
		remaining := parseTokenCount(ui.FreeCcuQuotaInfo.RemainingTokens)
		w := quotaWindow{
			Label:      "免费 CCU 剩余",
			Remaining:  float64(remaining),
			Used:       0,
			Total:      0,
			Unit:       "mCCUs",
			StatusText: "免费额度（毫 CCU）",
		}
		if ui.FreeCcuQuotaInfo.NextRefillTimestampSec > 0 {
			w.ResetInSec = int(ui.FreeCcuQuotaInfo.NextRefillTimestampSec - float64(time.Now().Unix()))
			if w.ResetInSec < 0 {
				w.ResetInSec = 0
			}
			w.ResetAt = time.Unix(int64(ui.FreeCcuQuotaInfo.NextRefillTimestampSec), 0).UTC().Format(time.RFC3339)
		}
		windows = append(windows, w)
	}

	acct.Cache.Windows = windows

	// Resolve email if missing
	if acct.Email == "" {
		if email, err := colabFetchEmail(acct.AccessToken); err == nil && email != "" {
			acct.Email = email
			persistColabAccount(acct.Name, acct.RefreshToken, email)
		}
	}

	// Record a snapshot of the current balances for the history chart.
	recordColabSnapshot(acct.Name, paidBalance, ui.FreeCcuQuotaInfo)
}

// recordColabSnapshot writes one point into colab_usage_history for the given
// account. It dedupes consecutive identical values within a small window so the
// chart doesn't get flooded with plateau points.
func recordColabSnapshot(account string, paidBalance *float64, freeInfo *struct {
	RemainingTokens        string  `json:"remainingTokens"`
	NextRefillTimestampSec float64 `json:"nextRefillTimestampSec"`
}) {
	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil {
		return
	}
	paid := 0.0
	hasPaid := 0
	if paidBalance != nil {
		paid = *paidBalance
		hasPaid = 1
	}
	free := 0.0
	hasFree := 0
	if freeInfo != nil {
		free = float64(parseTokenCount(freeInfo.RemainingTokens))
		hasFree = 1
	}
	// Throttle: skip if the newest stored point for this account is within
	// the last 3 minutes (prevents write amplification from frequent polls).
	var lastTs string
	d.QueryRow("SELECT ts FROM colab_usage_history WHERE account=? ORDER BY id DESC LIMIT 1", account).Scan(&lastTs)
	if lastTs != "" {
		if t, err := time.Parse(time.RFC3339, lastTs); err == nil && time.Since(t) < 3*time.Minute {
			return
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	d.Exec(`INSERT INTO colab_usage_history (account, ts, paid_balance, free_remaining, has_paid, has_free)
		VALUES (?, ?, ?, ?, ?, ?)`,
		account, now, paid, free, hasPaid, hasFree)
	// Prune: keep the newest 5000 points per account (~week of 1-min polls).
	d.Exec(`DELETE FROM colab_usage_history WHERE account=? AND id NOT IN (
		SELECT id FROM colab_usage_history WHERE account=? ORDER BY id DESC LIMIT 5000
	)`, account, account)
}

// colabTierLabel maps the raw subscriptionTier value from the Colab user-info
// endpoint to a human-readable label. The API can return several spellings:
// GAPI enum full names (SUBSCRIPTION_TIER_PRO), short names (PRO / PRO_PLUS),
// or numeric backend values (0/1/2). Mirrors colab-vscode's normalizeSubTier:
//
//	ColabSubscriptionTier.PRO(1)        → PRO
//	ColabSubscriptionTier.VERY_PRO(2)   → PRO_PLUS
//	ColabGapiSubscriptionTier.PRO       → PRO
//	ColabGapiSubscriptionTier.PRO_PLUS  → PRO_PLUS
func colabTierLabel(tier string) string {
	switch strings.ToUpper(strings.TrimSpace(tier)) {
	case "SUBSCRIPTION_TIER_PRO", "PRO", "1":
		return "Colab Pro"
	case "SUBSCRIPTION_TIER_PRO_PLUS", "PRO_PLUS", "VERY_PRO", "2":
		return "Colab Pro+"
	default:
		return "Colab 免费版"
	}
}

// computeColabCeiling derives the quota ceiling (total) and consumed units (used)
// for a Google Colab account, matching the Colab compute unit contract:
//
//   - Pro: 100 CCU/month (valid 90 days, max 300 rollover from subscription).
//   - Pro+: 600 CCU/month (valid 90 days, max 1800 rollover from subscription).
//   - Pay-as-you-go add-ons: strictly 100 or 500 CCU packs.
//   - Enterprise: unmetered / no ceiling.
//
// Because the Google API only exposes the single scalar `paidComputeUnitsBalance`
// and does not return past grant batches or expiration timestamps, this ladder
// function determines the current tier ceiling and derived consumption.
func computeColabCeiling(tier string, balance float64) (total float64, used float64, hint string) {
	t := strings.ToUpper(strings.TrimSpace(tier))
	switch {
	case strings.Contains(t, "ENTERPRISE"):
		return balance, 0.0, "企业版无固定上限 · 按量结算"
	case strings.Contains(t, "PRO_PLUS") || t == "VERY_PRO" || t == "2":
		base := 600.0
		if balance <= base {
			return base, math.Max(0.0, base-balance), "月度配额: 600 CCU / 月 (90天有效)"
		}
		ceil := math.Ceil(balance/100.0) * 100.0
		return ceil, math.Max(0.0, ceil-balance), fmt.Sprintf("月度配额 + 累积/增购 (上限 %.0f CCU)", ceil)
	case strings.Contains(t, "PRO") || t == "1":
		base := 100.0
		if balance <= base {
			return base, math.Max(0.0, base-balance), "月度配额: 100 CCU / 月 (90天有效)"
		}
		ceil := math.Ceil(balance/100.0) * 100.0
		return ceil, math.Max(0.0, ceil-balance), fmt.Sprintf("月度配额 + 累积/增购 (上限 %.0f CCU)", ceil)
	default:
		if balance <= 0 {
			return 0.0, 0.0, "无可用付费算力"
		}
		ceil := math.Ceil(balance/100.0) * 100.0
		return ceil, math.Max(0.0, ceil-balance), "Pay-As-You-Go 算力包 (100/500 CCU)"
	}
}

func parseTokenCount(s string) int64 {
	if s == "" {
		return 0
	}
	// ProtoJSON returns Int64 as a string; strip any trailing unit suffix.
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, "s")
	s = strings.TrimRight(s, "m")
	s = strings.TrimRight(s, "c")
	s = strings.TrimRight(s, "u")
	s = strings.TrimRight(s, "q")
	var n int64
	fmt.Sscanf(s, "%d", &n)
	return n
}

func colabFetchEmail(accessToken string) (string, error) {
	req, _ := http.NewRequest("GET", colabUserInfoEndpoint, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := colabHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("userinfo HTTP %d", resp.StatusCode)
	}
	var ui struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&ui); err != nil {
		return "", err
	}
	return ui.Email, nil
}

// ---------------------------------------------------------------------------
// Loopback login server
// ---------------------------------------------------------------------------

func startColabLoginServer(account string) (authURL, loginID string, err error) {
	colabLoginMu.Lock()
	defer colabLoginMu.Unlock()

	// Clean up stale logins
	for id, p := range colabLoginState {
		if time.Since(p.StartedAt) > time.Duration(colabLoginTimeoutSec)*time.Second {
			if p.Server != nil {
				p.Server.Close()
			}
			delete(colabLoginState, id)
		}
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", fmt.Errorf("failed to open loopback listener: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d", port)

	nonce := generateNonce()
	codeVerifier := generateCodeVerifier()
	challenge := generateCodeChallenge(codeVerifier)

	loginID = nonce
	pending := &colabLoginPending{
		Nonce:        nonce,
		CodeVerifier: codeVerifier,
		RedirectURI:  redirectURI,
		Account:      account,
		StartedAt:    time.Now(),
		ServerDone:   make(chan struct{}),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")
		if code == "" || state != "nonce="+nonce {
			w.WriteHeader(400)
			fmt.Fprint(w, "Colab 授权失败：缺少 code 或 state 不匹配")
			return
		}
		colabLoginMu.Lock()
		if p, ok := colabLoginState[loginID]; ok {
			p.Code = code
			p.CodeReceived = true
		}
		colabLoginMu.Unlock()
		w.WriteHeader(200)
		w.Write([]byte("<html><head><meta charset='utf-8'></head><body style='font-family:sans-serif;text-align:center;padding-top:80px'><h2>✅ Google Colab 授权成功</h2><p>可以关闭此页面，返回 Usage Keeper 仪表盘查看额度。</p></body></html>"))
	})

	server := &http.Server{Handler: mux}
	pending.Server = server
	colabLoginState[loginID] = pending

	go func() {
		_ = server.Serve(listener)
		close(pending.ServerDone)
	}()

	authURL = fmt.Sprintf("%s?%s", colabAuthEndpoint, url.Values{
		"client_id":             {colabOAuthClientID},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"scope":                 {strings.Join(colabRequiredScopes, " ")},
		"access_type":           {"offline"},
		"include_granted_scopes": {"true"},
		"state":                 {"nonce=" + nonce},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"prompt":                {"consent"},
	}.Encode())

	return authURL, loginID, nil
}

// checkColabLoginCode polls for a received code; if present, exchanges it,
// stores credentials, and refreshes quota. Returns the collected login result.
func checkColabLoginCode(loginID string) (bool, string, error) {
	colabLoginMu.Lock()
	p, ok := colabLoginState[loginID]
	code := ""
	done := false
	if ok {
		code = p.Code
		done = p.CodeReceived
	}
	colabLoginMu.Unlock()
	if !ok {
		return false, "", fmt.Errorf("login session not found")
	}
	if !done || code == "" {
		return false, "", nil // still waiting
	}
	// Exchange code
	tr, err := colabExchangeCode(code, p.CodeVerifier, p.RedirectURI)
	if err != nil {
		colabLoginMu.Lock()
		delete(colabLoginState, loginID)
		colabLoginMu.Unlock()
		return false, "", err
	}
	if tr.RefreshToken == "" {
		colabLoginMu.Lock()
		delete(colabLoginState, loginID)
		colabLoginMu.Unlock()
		return false, "", fmt.Errorf("no refresh_token returned — revoke access via Google and retry")
	}
	// Store account
	email := ""
	if email, _ = colabFetchEmail(tr.AccessToken); email == "" {
		email = "colab-" + p.Account
	}
	colabAcctMu.Lock()
	var acct *colabAccountRuntime
	for _, a := range colabAccounts {
		if a.Name == p.Account {
			acct = a
			break
		}
	}
	if acct == nil {
		acct = &colabAccountRuntime{
			Name: p.Account,
			Cache: quotaAccount{
				Type:    "colab",
				Name:    p.Account,
				Success: false,
				Windows: []quotaWindow{},
				Error:   "not fetched yet",
			},
		}
		colabAccounts = append(colabAccounts, acct)
	}
	acct.RefreshToken = tr.RefreshToken
	acct.Email = email
	acct.AccessToken = tr.AccessToken
	ttl := tr.ExpiresIn
	if ttl <= 0 {
		ttl = 3600
	}
	acct.TokenExpiry = time.Now().Add(time.Duration(ttl) * time.Second)
	colabAcctMu.Unlock()

	persistColabAccount(p.Account, tr.RefreshToken, email)

	// Clean up login state and server
	colabLoginMu.Lock()
	if p.Server != nil {
		p.Server.Close()
	}
	delete(colabLoginState, loginID)
	colabLoginMu.Unlock()

	go refreshColabQuota(acct)
	return true, email, nil
}

func colabLoginAccountExists(name string) bool {
	colabAcctMu.RLock()
	defer colabAcctMu.RUnlock()
	for _, a := range colabAccounts {
		if a.Name == name {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func getColabAccount(name string) *colabAccountRuntime {
	colabAcctMu.RLock()
	defer colabAcctMu.RUnlock()
	for _, a := range colabAccounts {
		if a.Name == name {
			return a
		}
	}
	return nil
}

func handleColabQuotaGet(query map[string][]string) pluginapi.ManagementResponse {
	if action, ok := getQueryParam(query, "action"); ok && action != "" {
		action = strings.ToLower(strings.TrimSpace(action))
		acctName, _ := getQueryParam(query, "account")
		acctName = strings.TrimSpace(acctName)
		loginID, _ := getQueryParam(query, "login_id")

		switch action {
		case "login":
			if acctName == "" {
				return jsonResponse(http.StatusBadRequest, map[string]string{"error": "account name is required"})
			}
			if colabLoginAccountExists(acctName) {
				return jsonResponse(http.StatusConflict, map[string]string{"error": "account already exists"})
			}
			authURL, id, err := startColabLoginServer(acctName)
			if err != nil {
				return jsonResponse(http.StatusInternalServerError, map[string]string{"error": err.Error()})
			}
			return jsonResponse(http.StatusOK, map[string]any{
				"auth_url": authURL,
				"login_id": id,
				"account":  acctName,
				"redirect_uri": fmt.Sprintf("http://127.0.0.1:<port>/?code=...&state=nonce=%s", id),
			})
		case "loginstate":
			if loginID == "" {
				return jsonResponse(http.StatusBadRequest, map[string]string{"error": "login_id is required"})
			}
			done, email, err := checkColabLoginCode(loginID)
			if err != nil {
				return jsonResponse(http.StatusOK, map[string]any{"done": false, "error": err.Error()})
			}
			return jsonResponse(http.StatusOK, map[string]any{"done": done, "email": email})
		case "addaccount":
			// Direct credentials (refresh_token) entry — used as fallback when
			// loopback is unavailable.
			if acctName == "" {
				return jsonResponse(http.StatusBadRequest, map[string]string{"error": "account name is required"})
			}
			refreshToken, _ := getQueryParam(query, "refresh_token")
			refreshToken = strings.TrimSpace(refreshToken)
			if refreshToken == "" {
				return jsonResponse(http.StatusBadRequest, map[string]string{"error": "refresh_token is required"})
			}
			if colabLoginAccountExists(acctName) {
				return jsonResponse(http.StatusConflict, map[string]string{"error": "account already exists"})
			}
			acct := &colabAccountRuntime{
				Name:         acctName,
				RefreshToken: refreshToken,
				Cache: quotaAccount{
					Type:    "colab",
					Name:    acctName,
					Success: false,
					Windows: []quotaWindow{},
					Error:   "not fetched yet",
				},
			}
			colabAcctMu.Lock()
			colabAccounts = append(colabAccounts, acct)
			colabAcctMu.Unlock()
			persistColabAccount(acctName, refreshToken, "")
			go refreshColabQuota(acct)
		case "removeaccount":
			colabAcctMu.Lock()
			for i, a := range colabAccounts {
				if a.Name == acctName {
					colabAccounts = append(colabAccounts[:i], colabAccounts[i+1:]...)
					break
				}
			}
			colabAcctMu.Unlock()
			deleteColabAccountFromDB(acctName)
		case "refresh":
			acct := getColabAccount(acctName)
			if acct == nil {
				return jsonResponse(http.StatusNotFound, map[string]string{"error": "account not found"})
			}
			if acct.RefreshToken == "" {
				return jsonResponse(http.StatusBadRequest, map[string]string{"error": "no credentials configured"})
			}
			go refreshColabQuota(acct)
		case "history":
			days := 30
			if v, ok := getQueryParam(query, "days"); ok && v != "" {
				if n := parseIntVal(v); n > 0 && n <= 365 {
					days = n
				}
			}
			return handleColabHistory(acctName, days)
		default:
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "unknown action"})
		}
	}

	colabAcctMu.RLock()
	accounts := make([]quotaAccount, 0, len(colabAccounts))
	for _, a := range colabAccounts {
		a.CacheMu.RLock()
		acct := a.Cache
		a.CacheMu.RUnlock()
		acct.Name = a.Name
		acct.Type = "colab"
		accounts = append(accounts, acct)
	}
	colabAcctMu.RUnlock()
	return jsonResponse(http.StatusOK, quotaResponse{Accounts: accounts})
}

// handleColabHistory returns snapshot history points for an account.
// Response shape: { account, days, points: [{ts, paid_balance, free_remaining, has_paid, has_free}] }
func handleColabHistory(account string, days int) pluginapi.ManagementResponse {
	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil {
		return jsonResponse(http.StatusOK, map[string]any{"account": account, "days": days, "points": []any{}})
	}
	if account == "" {
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "account is required"})
	}
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339)
	rows, err := d.Query(
		`SELECT ts, paid_balance, free_remaining, has_paid, has_free
		 FROM colab_usage_history
		 WHERE account = ? AND ts >= ?
		 ORDER BY id ASC`,
		account, since,
	)
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]string{"error": "query failed"})
	}
	defer rows.Close()

	type point struct {
		TS            string  `json:"ts"`
		PaidBalance   float64 `json:"paid_balance"`
		FreeRemaining float64 `json:"free_remaining"`
		HasPaid       bool    `json:"has_paid"`
		HasFree       bool    `json:"has_free"`
	}
	points := make([]point, 0, 256)
	for rows.Next() {
		var p point
		var paid float64
		var free float64
		var hp int
		var hf int
		if err := rows.Scan(&p.TS, &paid, &free, &hp, &hf); err == nil {
			p.PaidBalance = paid
			p.FreeRemaining = free
			p.HasPaid = hp != 0
			p.HasFree = hf != 0
			points = append(points, p)
		}
	}
	return jsonResponse(http.StatusOK, map[string]any{
		"account": account,
		"days":    days,
		"points":  points,
	})
}

// handleColabQuotaPost accepts JSON body actions (mirrors other providers).
func handleColabQuotaPost(body []byte) pluginapi.ManagementResponse {
	var req struct {
		Action       string `json:"action"`
		Account      string `json:"account"`
		RefreshToken string `json:"refresh_token"`
		Email        string `json:"email"`
		LoginID      string `json:"login_id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
	}
	acctName := strings.TrimSpace(req.Account)
	action := strings.ToLower(strings.TrimSpace(req.Action))

	switch action {
	case "login":
		return handleColabQuotaGet(map[string][]string{"action": {"login"}, "account": {acctName}})
	case "loginstate":
		return handleColabQuotaGet(map[string][]string{"action": {"loginstate"}, "login_id": {req.LoginID}})
	case "addaccount":
		return handleColabQuotaGet(map[string][]string{
			"action": {"addaccount"}, "account": {acctName}, "refresh_token": {req.RefreshToken},
		})
	case "removeaccount":
		return handleColabQuotaGet(map[string][]string{"action": {"removeaccount"}, "account": {acctName}})
	case "refresh":
		return handleColabQuotaGet(map[string][]string{"action": {"refresh"}, "account": {acctName}})
	default:
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "unknown action, use 'login', 'loginstate', 'refresh', 'addaccount', or 'removeaccount'"})
	}
}

// shutdownColabLoopback closes any in-flight login servers.
func shutdownColabLoopback() {
	colabLoginMu.Lock()
	defer colabLoginMu.Unlock()
	for id, p := range colabLoginState {
		if p.Server != nil {
			p.Server.Close()
		}
		delete(colabLoginState, id)
	}
}
