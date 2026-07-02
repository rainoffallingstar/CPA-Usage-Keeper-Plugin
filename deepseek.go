package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// ---------------------------------------------------------------------------
// DeepSeek balance monitoring
// ---------------------------------------------------------------------------

const deepseekBalanceTimeout = 10 * time.Second

type deepseekBalanceInfo struct {
	Currency        string `json:"currency"`
	TotalBalance    string `json:"total_balance"`
	GrantedBalance  string `json:"granted_balance"`
	ToppedUpBalance string `json:"topped_up_balance"`
}

type deepseekBalanceResp struct {
	IsAvailable  bool                  `json:"is_available"`
	BalanceInfos []deepseekBalanceInfo `json:"balance_infos"`
}

var deepseekHTTPClient = &http.Client{Timeout: deepseekBalanceTimeout}

func fetchDeepseekBalance(apiKey string) (*deepseekBalanceResp, error) {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return nil, fmt.Errorf("API key is empty")
	}
	req, _ := http.NewRequest("GET", "https://api.deepseek.com/user/balance", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")

	resp, err := deepseekHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var r deepseekBalanceResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

// ---------------------------------------------------------------------------
// State + lifecycle
// ---------------------------------------------------------------------------

type deepseekAccountRuntime struct {
	Name   string
	APIKey string
	Cache  quotaAccount
	CacheMu sync.RWMutex
}

var (
	deepseekAccounts []*deepseekAccountRuntime
	deepseekAcctMu   sync.RWMutex
)

func initDeepseekAccounts(cfgs []deepseekAcctCfg) {
	deepseekAcctMu.Lock()
	defer deepseekAcctMu.Unlock()

	seen := make(map[string]bool)
	for _, a := range deepseekAccounts {
		seen[a.Name] = true
	}
	for _, cfg := range cfgs {
		name := strings.TrimSpace(cfg.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		deepseekAccounts = append(deepseekAccounts, &deepseekAccountRuntime{
			Name: name, APIKey: strings.TrimSpace(cfg.APIKey),
			Cache: quotaAccount{Type: "deepseek", Name: name, Success: false, Windows: []quotaWindow{}, Error: "not fetched yet"},
		})
	}
	for _, a := range deepseekAccounts {
		if a.APIKey != "" {
			go refreshDeepseekBalance(a)
		}
	}
}

func refreshDeepseekBalance(acct *deepseekAccountRuntime) {
	bal, err := fetchDeepseekBalance(acct.APIKey)
	acct.CacheMu.Lock()
	defer acct.CacheMu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		acct.Cache.Success = false
		acct.Cache.Windows = nil
		acct.Cache.Error = err.Error()
		acct.Cache.UpdatedAt = now
		return
	}
	acct.Cache.Success = true
	acct.Cache.UpdatedAt = now
	acct.Cache.Error = ""
	acct.Cache.Windows = make([]quotaWindow, 0, len(bal.BalanceInfos)*3)
	for _, info := range bal.BalanceInfos {
		total, _ := strconv.ParseFloat(strings.TrimSpace(info.TotalBalance), 64)
		granted, _ := strconv.ParseFloat(strings.TrimSpace(info.GrantedBalance), 64)
		toppedUp, _ := strconv.ParseFloat(strings.TrimSpace(info.ToppedUpBalance), 64)

		acct.Cache.Windows = append(acct.Cache.Windows, quotaWindow{
			Label:     "Total Balance",
			Remaining: total,
			Total:     total,
			Unit:      info.Currency,
		})
		acct.Cache.Windows = append(acct.Cache.Windows, quotaWindow{
			Label:     "Granted",
			Remaining: granted,
			Total:     granted,
			Unit:      info.Currency,
		})
		acct.Cache.Windows = append(acct.Cache.Windows, quotaWindow{
			Label:     "Topped Up",
			Remaining: toppedUp,
			Total:     toppedUp,
			Unit:      info.Currency,
		})
	}
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func handleDeepseekQuotaGet(query map[string][]string) pluginapi.ManagementResponse {
	if action, ok := getQueryParam(query, "action"); ok && action != "" {
		acctName, _ := getQueryParam(query, "account")
		apiKey, _ := getQueryParam(query, "api_key")
		action = strings.ToLower(strings.TrimSpace(action))
		acctName = strings.TrimSpace(acctName)

		switch action {
		case "addaccount":
			if acctName == "" {
				return jsonResponse(http.StatusBadRequest, map[string]string{"error": "account name is required"})
			}
			deepseekAcctMu.Lock()
			for _, a := range deepseekAccounts {
				if a.Name == acctName {
					deepseekAcctMu.Unlock()
					return jsonResponse(http.StatusConflict, map[string]string{"error": "already exists"})
				}
			}
			r := &deepseekAccountRuntime{
				Name: acctName, APIKey: strings.TrimSpace(apiKey),
				Cache: quotaAccount{Type: "deepseek", Name: acctName, Success: false, Windows: []quotaWindow{}, Error: "not fetched yet"},
			}
			deepseekAccounts = append(deepseekAccounts, r)
			deepseekAcctMu.Unlock()
			persistDeepseekAccount(acctName, r.APIKey)
			if r.APIKey != "" {
				go refreshDeepseekBalance(r)
			}
		case "removeaccount":
			deepseekAcctMu.Lock()
			for i, a := range deepseekAccounts {
				if a.Name == acctName {
					deepseekAccounts = append(deepseekAccounts[:i], deepseekAccounts[i+1:]...)
					break
				}
			}
			deepseekAcctMu.Unlock()
			deleteDeepseekAccountFromDB(acctName)
		case "setkey":
			deepseekAcctMu.RLock()
			var t *deepseekAccountRuntime
			for _, a := range deepseekAccounts {
				if a.Name == acctName {
					t = a
					break
				}
			}
			deepseekAcctMu.RUnlock()
			if t == nil {
				return jsonResponse(http.StatusNotFound, map[string]string{"error": "not found"})
			}
			if strings.TrimSpace(apiKey) == "" {
				return jsonResponse(http.StatusBadRequest, map[string]string{"error": "api_key required"})
			}
			t.APIKey = strings.TrimSpace(apiKey)
			persistDeepseekAccount(t.Name, t.APIKey)
			go refreshDeepseekBalance(t)
		case "refresh":
			deepseekAcctMu.RLock()
			var t *deepseekAccountRuntime
			for _, a := range deepseekAccounts {
				if a.Name == acctName {
					t = a
					break
				}
			}
			deepseekAcctMu.RUnlock()
			if t == nil {
				return jsonResponse(http.StatusNotFound, map[string]string{"error": "not found"})
			}
			if t.APIKey == "" {
				return jsonResponse(http.StatusBadRequest, map[string]string{"error": "no API key"})
			}
			go refreshDeepseekBalance(t)
		default:
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "unknown action"})
		}
	}

	deepseekAcctMu.RLock()
	accounts := make([]quotaAccount, 0, len(deepseekAccounts))
	for _, a := range deepseekAccounts {
		a.CacheMu.RLock()
		acct := a.Cache
		a.CacheMu.RUnlock()
		acct.Name = a.Name
		acct.Type = "deepseek"
		accounts = append(accounts, acct)
	}
	deepseekAcctMu.RUnlock()
	return jsonResponse(http.StatusOK, quotaResponse{Accounts: accounts})
}

// ---------------------------------------------------------------------------
// DB persistence for DeepSeek accounts
// ---------------------------------------------------------------------------

func loadDeepseekAccountsFromDB() {
	defer func() { recover() }()

	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil {
		return
	}
	rows, _ := d.Query("SELECT name, api_key FROM deepseek_accounts")
	if rows == nil {
		return
	}
	defer rows.Close()
	deepseekAcctMu.Lock()
	defer deepseekAcctMu.Unlock()
	for rows.Next() {
		var name, key string
		if rows.Scan(&name, &key) != nil {
			continue
		}
		seen := false
		for _, a := range deepseekAccounts {
			if a.Name == name {
				seen = true
				break
			}
		}
		if seen {
			continue
		}
		deepseekAccounts = append(deepseekAccounts, &deepseekAccountRuntime{
			Name: name, APIKey: key,
			Cache: quotaAccount{Type: "deepseek", Name: name, Success: false, Windows: []quotaWindow{}, Error: "not fetched yet"},
		})
		if key != "" {
			go refreshDeepseekBalance(deepseekAccounts[len(deepseekAccounts)-1])
		}
	}
}

func persistDeepseekAccount(name, key string) {
	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil {
		return
	}
	d.Exec(`INSERT INTO deepseek_accounts (name, api_key) VALUES (?, ?)
		ON CONFLICT(name) DO UPDATE SET api_key=excluded.api_key`, name, key)
}

func deleteDeepseekAccountFromDB(name string) {
	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil {
		return
	}
	d.Exec("DELETE FROM deepseek_accounts WHERE name = ?", name)
}
