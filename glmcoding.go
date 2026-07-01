package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// ---------------------------------------------------------------------------
// GLM Coding Plan quota monitoring
// ---------------------------------------------------------------------------

const glmCodingQuotaTimeout = 10 * time.Second

type glmQuotaLimitItem struct {
	Type          string  `json:"type"`
	Used          float64 `json:"used"`
	Total         float64 `json:"total"`
	Percentage    float64 `json:"percentage"`
	CurrentValue  float64 `json:"currentValue"`
	TimeUsed      float64 `json:"timeUsed"`
	TimeTotal     float64 `json:"timeTotal"`
}

type glmQuotaLimitResp struct {
	Data struct {
		Limits []glmQuotaLimitItem `json:"limits"`
	} `json:"data"`
}

type glmModelUsageItem struct {
	Model  string `json:"model"`
	Tokens int64  `json:"tokens"`
}

type glmModelUsageResp struct {
	Data []glmModelUsageItem `json:"data"`
}

var glmHTTPClient = &http.Client{Timeout: glmCodingQuotaTimeout}

func fetchGlmCodingQuota(apiKey, baseURL string) (*quotaAccount, error) {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return nil, fmt.Errorf("API key is empty")
	}
	u := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if u == "" {
		u = "https://api.z.ai/api/anthropic"
	}
	// Strip the anthropic path suffix to get the base domain
	base := strings.TrimSuffix(strings.TrimSuffix(u, "/api/anthropic"), "/v1")
	if strings.HasSuffix(base, "/api") {
		base = strings.TrimSuffix(base, "/api")
	}

	// Time window: yesterday at current hour to now
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day()-1, now.Hour(), 0, 0, 0, now.Location())
	end := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 59, 59, 999, now.Location())

	startStr := start.Format("2006-01-02 15:04:05")
	endStr := end.Format("2006-01-02 15:04:05")

	var windows []quotaWindow
	var modelUsage []glmModelUsage

	// 1. Quota limits
	quotaURL := fmt.Sprintf("%s/api/monitor/usage/quota/limit", base)
	quotas, err := fetchGlmQuotaLimits(quotaURL, key)
	if err != nil {
		return nil, fmt.Errorf("quota query failed: %w", err)
	}
	for _, q := range quotas {
		w := quotaWindow{
			Remaining: q.Percentage,
			Used:      q.Used,
			Total:     q.Total,
			Unit:      "%",
		}
		if q.Type == "TOKENS_LIMIT" {
			w.Label = "Token (5h)"
		} else if q.Type == "TIME_LIMIT" {
			w.Label = "MCP (1M)"
		} else {
			w.Label = q.Type
		}
		windows = append(windows, w)
	}

	// 2. Model usage
	modelURL := fmt.Sprintf("%s/api/monitor/usage/model-usage?startTime=%s&endTime=%s", base, encodeQuery(startStr), encodeQuery(endStr))
	modelUsage, _ = fetchGlmModelUsage(modelURL, key)

	return &quotaAccount{
		Type:       "glm_coding",
		Success:    true,
		UpdatedAt:  now.UTC().Format(time.RFC3339),
		Windows:    windows,
		ModelUsage: modelUsage,
	}, nil
}

func fetchGlmQuotaLimits(url, key string) ([]glmQuotaLimitItem, error) {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", key)
	resp, err := glmHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var r glmQuotaLimitResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	var limits []glmQuotaLimitItem
	for _, l := range r.Data.Limits {
		l.Percentage = 100 - l.Percentage // API returns used%, we want remaining%
		if l.Percentage < 0 {
			l.Percentage = 0
		}
		limits = append(limits, l)
	}
	return limits, nil
}

func fetchGlmModelUsage(url, key string) ([]glmModelUsage, error) {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", key)
	resp, err := glmHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var r glmModelUsageResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	var usage []glmModelUsage
	for _, item := range r.Data {
		usage = append(usage, glmModelUsage{Model: item.Model, Tokens: item.Tokens})
	}
	return usage, nil
}

// ---------------------------------------------------------------------------
// State + lifecycle
// ---------------------------------------------------------------------------

type glmCodingRuntime struct {
	Name    string
	APIKey  string
	BaseURL string
	Cache   quotaAccount
	CacheMu sync.RWMutex
}

var (
	glmAccounts  []*glmCodingRuntime
	glmAcctMu    sync.RWMutex
)

func initGlmCodingAccounts(cfgs []glmCodingAcctCfg) {
	glmAcctMu.Lock()
	defer glmAcctMu.Unlock()

	seen := make(map[string]bool)
	for _, a := range glmAccounts {
		seen[a.Name] = true
	}
	for _, cfg := range cfgs {
		name := strings.TrimSpace(cfg.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		glmAccounts = append(glmAccounts, &glmCodingRuntime{
			Name:    name,
			APIKey:  strings.TrimSpace(cfg.APIKey),
			BaseURL: strings.TrimSpace(cfg.BaseURL),
			Cache: quotaAccount{
				Type:    "glm_coding",
				Name:    name,
				Success: false,
				Windows: []quotaWindow{},
				Error:   "not fetched yet",
			},
		})
	}

	// Immediately refresh all accounts with API keys
	for _, a := range glmAccounts {
		if a.APIKey != "" {
			go refreshGlmQuota(a)
		}
	}
}

func refreshGlmQuota(acct *glmCodingRuntime) {
	quota, err := fetchGlmCodingQuota(acct.APIKey, acct.BaseURL)
	acct.CacheMu.Lock()
	defer acct.CacheMu.Unlock()
	if err != nil {
		acct.Cache.Success = false
		acct.Cache.Windows = nil
		acct.Cache.ModelUsage = nil
		acct.Cache.Error = err.Error()
		acct.Cache.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	} else {
		quota.Name = acct.Name
		acct.Cache = *quota
	}
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func handleGlmCodingQuotaGet(query map[string][]string) pluginapi.ManagementResponse {
	if action, ok := getQueryParam(query, "action"); ok && action != "" {
		acctName, _ := getQueryParam(query, "account")
		apiKey, _ := getQueryParam(query, "api_key")
		baseURL, _ := getQueryParam(query, "base_url")
		action = strings.ToLower(strings.TrimSpace(action))
		acctName = strings.TrimSpace(acctName)

		switch action {
		case "addaccount":
			if acctName == "" {
				return jsonResponse(http.StatusBadRequest, map[string]string{"error": "account name is required"})
			}
			glmAcctMu.Lock()
			for _, a := range glmAccounts {
				if a.Name == acctName {
					glmAcctMu.Unlock()
					return jsonResponse(http.StatusConflict, map[string]string{"error": "account already exists"})
				}
			}
			r := &glmCodingRuntime{
				Name:    acctName,
				APIKey:  strings.TrimSpace(apiKey),
				BaseURL: strings.TrimSpace(baseURL),
				Cache: quotaAccount{
					Type:    "glm_coding",
					Name:    acctName,
					Success: false,
					Windows: []quotaWindow{},
					Error:   "not fetched yet",
				},
			}
			glmAccounts = append(glmAccounts, r)
			glmAcctMu.Unlock()
			persistGlmAccount(acctName, r.APIKey, r.BaseURL)
			if r.APIKey != "" {
				go refreshGlmQuota(r)
			}
		case "removeaccount":
			glmAcctMu.Lock()
			for i, a := range glmAccounts {
				if a.Name == acctName {
					glmAccounts = append(glmAccounts[:i], glmAccounts[i+1:]...)
					break
				}
			}
			glmAcctMu.Unlock()
			deleteGlmAccountFromDB(acctName)
		case "setkey":
			glmAcctMu.RLock()
			var target *glmCodingRuntime
			for _, a := range glmAccounts {
				if a.Name == acctName { target = a; break }
			}
			glmAcctMu.RUnlock()
			if target == nil {
				return jsonResponse(http.StatusNotFound, map[string]string{"error": "account not found"})
			}
			if strings.TrimSpace(apiKey) == "" {
				return jsonResponse(http.StatusBadRequest, map[string]string{"error": "api_key is required"})
			}
			target.APIKey = strings.TrimSpace(apiKey)
			target.BaseURL = strings.TrimSpace(baseURL)
			persistGlmAccount(target.Name, target.APIKey, target.BaseURL)
			go refreshGlmQuota(target)
		case "refresh":
			glmAcctMu.RLock()
			var target *glmCodingRuntime
			for _, a := range glmAccounts {
				if a.Name == acctName { target = a; break }
			}
			glmAcctMu.RUnlock()
			if target == nil {
				return jsonResponse(http.StatusNotFound, map[string]string{"error": "account not found"})
			}
			if target.APIKey == "" {
				return jsonResponse(http.StatusBadRequest, map[string]string{"error": "no API key configured"})
			}
			go refreshGlmQuota(target)
		default:
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "unknown action"})
		}
	}

	glmAcctMu.RLock()
	accounts := make([]quotaAccount, 0, len(glmAccounts))
	for _, a := range glmAccounts {
		a.CacheMu.RLock()
		acct := a.Cache
		a.CacheMu.RUnlock()
		acct.Name = a.Name
		acct.Type = "glm_coding"
		accounts = append(accounts, acct)
	}
	glmAcctMu.RUnlock()
	return jsonResponse(http.StatusOK, quotaResponse{Accounts: accounts})
}

func encodeQuery(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, " ", "%20"), ":", "%3A")
}

// ---------------------------------------------------------------------------
// DB persistence for GLM Coding accounts
// ---------------------------------------------------------------------------

func loadGlmAccountsFromDB() {
	defer func() { recover() }()

	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil { return }
	rows, _ := d.Query("SELECT name, api_key, base_url FROM glm_coding_accounts")
	if rows == nil { return }
	defer rows.Close()
	glmAcctMu.Lock()
	defer glmAcctMu.Unlock()
	for rows.Next() {
		var name, key, base string
		if rows.Scan(&name, &key, &base) != nil { continue }
		seen := false
		for _, a := range glmAccounts {
			if a.Name == name { seen = true; break }
		}
		if seen { continue }
		glmAccounts = append(glmAccounts, &glmCodingRuntime{
			Name: name, APIKey: key, BaseURL: base,
			Cache: quotaAccount{Type: "glm_coding", Name: name, Success: false, Windows: []quotaWindow{}, Error: "not fetched yet"},
		})
		if key != "" { go refreshGlmQuota(glmAccounts[len(glmAccounts)-1]) }
	}
}

func persistGlmAccount(name, key, baseURL string) {
	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil { return }
	d.Exec(`INSERT INTO glm_coding_accounts (name, api_key, base_url) VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET api_key=excluded.api_key, base_url=excluded.base_url`, name, key, baseURL)
}

func deleteGlmAccountFromDB(name string) {
	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil { return }
	d.Exec("DELETE FROM glm_coding_accounts WHERE name = ?", name)
}
