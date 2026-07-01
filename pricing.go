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

type modelPrice struct {
	Prompt     float64 `json:"prompt"`
	Completion float64 `json:"completion"`
	Cache      float64 `json:"cache"`
	AutoSynced bool    `json:"auto_synced"`
}

type pricesResponse struct {
	Prices    map[string]modelPrice `json:"prices"`
	LastSync  string                `json:"last_sync,omitempty"`
	SyncedAt  string                `json:"synced_at,omitempty"`
}

var pricesMu sync.RWMutex
var pricesStore = make(map[string]modelPrice)

// ---------------------------------------------------------------------------
// DB persistence
// ---------------------------------------------------------------------------

func loadPricesFromDB() {
	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil {
		return
	}
	rows, err := d.Query("SELECT model, prompt, completion, cache, auto_synced FROM model_prices")
	if err != nil {
		return
	}
	defer rows.Close()
	pricesMu.Lock()
	for rows.Next() {
		var model string
		var mp modelPrice
		var as int
		if rows.Scan(&model, &mp.Prompt, &mp.Completion, &mp.Cache, &as) == nil {
			mp.AutoSynced = as != 0
			pricesStore[model] = mp
		}
	}
	pricesMu.Unlock()
}

func persistPrice(model string, mp modelPrice) {
	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil {
		return
	}
	as := 0
	if mp.AutoSynced {
		as = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	d.Exec(`INSERT INTO model_prices (model, prompt, completion, cache, auto_synced, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(model) DO UPDATE SET prompt=excluded.prompt, completion=excluded.completion, cache=excluded.cache, auto_synced=excluded.auto_synced, updated_at=excluded.updated_at`,
		model, mp.Prompt, mp.Completion, mp.Cache, as, now)
}

func deletePriceFromDB(model string) {
	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil {
		return
	}
	d.Exec("DELETE FROM model_prices WHERE model = ?", model)
}

func handleGetPrices() pluginapi.ManagementResponse {
	pricesMu.RLock()
	prices := make(map[string]modelPrice, len(pricesStore))
	for k, v := range pricesStore {
		prices[k] = v
	}
	pricesMu.RUnlock()
	return jsonResponse(http.StatusOK, pricesResponse{Prices: prices})
}

func handlePutPrice(body []byte) pluginapi.ManagementResponse {
	var payload struct {
		Model string     `json:"model"`
		Price modelPrice `json:"price"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
	}
	if strings.TrimSpace(payload.Model) == "" {
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "model is required"})
	}
	pricesMu.Lock()
	pricesStore[payload.Model] = payload.Price
	pricesMu.Unlock()
	persistPrice(payload.Model, payload.Price)
	return handleGetPrices()
}

func handleDeletePrice(query map[string][]string) pluginapi.ManagementResponse {
	model := ""
	if v, ok := query["model"]; ok && len(v) > 0 {
		model = v[0]
	}
	if model == "" {
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "model query parameter required"})
	}
	pricesMu.Lock()
	delete(pricesStore, model)
	pricesMu.Unlock()
	deletePriceFromDB(model)
	return handleGetPrices()
}

func handleGetPricesWithActions(query map[string][]string) pluginapi.ManagementResponse {
	action := ""
	if v, ok := query["action"]; ok && len(v) > 0 {
		action = strings.ToLower(strings.TrimSpace(v[0]))
	}
	model := ""
	if v, ok := query["model"]; ok && len(v) > 0 {
		model = strings.TrimSpace(v[0])
	}

	switch action {
	case "set":
		if model == "" {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "model is required"})
		}
		pr := modelPrice{}
		if v, ok := query["prompt"]; ok && len(v) > 0 {
			fmt.Sscanf(strings.TrimSpace(v[0]), "%f", &pr.Prompt)
		}
		if v, ok := query["completion"]; ok && len(v) > 0 {
			fmt.Sscanf(strings.TrimSpace(v[0]), "%f", &pr.Completion)
		}
		if v, ok := query["cache"]; ok && len(v) > 0 {
			fmt.Sscanf(strings.TrimSpace(v[0]), "%f", &pr.Cache)
		}
		if pr.Prompt == 0 && pr.Completion == 0 && pr.Cache == 0 {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "at least one price field is required"})
		}
		pricesMu.Lock()
		pricesStore[model] = pr
		pricesMu.Unlock()
		persistPrice(model, pr)
	case "delete":
		if model == "" {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "model is required"})
		}
		pricesMu.Lock()
		delete(pricesStore, model)
		pricesMu.Unlock()
		deletePriceFromDB(model)
	}

	return handleGetPrices()
}

func handlePricesPost(body []byte) pluginapi.ManagementResponse {
	var req struct {
		Action string     `json:"action"`
		Model  string     `json:"model"`
		Price  modelPrice `json:"price"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	model := strings.TrimSpace(req.Model)
	if model == "" {
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "model is required"})
	}
	switch action {
	case "set", "":
		if req.Price.Prompt == 0 && req.Price.Completion == 0 && req.Price.Cache == 0 {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "at least one price field is required"})
		}
		pricesMu.Lock()
		pricesStore[model] = req.Price
		pricesMu.Unlock()
		persistPrice(model, req.Price)
	case "delete":
		pricesMu.Lock()
		delete(pricesStore, model)
		pricesMu.Unlock()
		deletePriceFromDB(model)
	default:
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "unknown action, use 'set' or 'delete'"})
	}
	return handleGetPrices()
}

func computeCost(model string, inputTokens, outputTokens, cachedTokens int64) float64 {
	pricesMu.RLock()
	price, ok := matchPrice(model)
	pricesMu.RUnlock()
	if !ok {
		return 0
	}
	cached := float64(cachedTokens)
	input := float64(inputTokens)
	if cached > input {
		input = 0
	} else {
		input -= cached
	}
	return input/1e6*price.Prompt + float64(outputTokens)/1e6*price.Completion + cached/1e6*price.Cache
}

// matchPrice looks up a model name with fuzzy matching against pricesStore.
func matchPrice(model string) (modelPrice, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return modelPrice{}, false
	}
	// Exact match first
	if p, ok := pricesStore[model]; ok {
		return p, true
	}
	// Try lowercase
	lower := strings.ToLower(model)
	if p, ok := pricesStore[lower]; ok {
		return p, true
	}
	// Generate variants (dot↔dash, no dashes, no dots)
	for _, v := range modelVariants(lower) {
		if p, ok := pricesStore[v]; ok {
			return p, true
		}
	}
	// Try stripping common prefixes (openai/, anthropic/, etc.)
	if idx := strings.Index(model, "/"); idx >= 0 {
		return matchPrice(model[idx+1:])
	}
	return modelPrice{}, false
}

// modelVariants generates common spelling variations of a model name.
func modelVariants(name string) []string {
	var out []string
	add := func(s string) { out = append(out, s) }
	// dot↔dash interchange
	if strings.Contains(name, ".") {
		add(strings.ReplaceAll(name, ".", "-"))
	}
	if strings.Contains(name, "-") {
		add(strings.ReplaceAll(name, "-", "."))
	}
	// remove all separators
	add(strings.ReplaceAll(strings.ReplaceAll(name, "-", ""), ".", ""))
	// insert possible missing dashes between number-letter boundaries
	// e.g. "glm5.2" → "glm-5.2", "deepseekv4" → "deepseek-v4"
	return out
}

// ---------------------------------------------------------------------------
// Auto-sync pricing from modelprice.boxtech.icu
// ---------------------------------------------------------------------------

const modelPriceAPI = "https://modelprice.boxtech.icu/api/v2/entities"

type mpEntity struct {
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Primary  struct {
		Provider string `json:"provider"`
		Pricing  struct {
			Input      *float64 `json:"input"`
			Output     *float64 `json:"output"`
			CacheRead  *float64 `json:"cache_read"`
		} `json:"pricing"`
	} `json:"primary_offering"`
}

var mpClient = &http.Client{Timeout: 30 * time.Second}

func syncModelPrices() (int, error) {
	resp, err := mpClient.Get(modelPriceAPI)
	if err != nil {
		return 0, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("modelprice API returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20))
	if err != nil {
		return 0, fmt.Errorf("read body failed: %w", err)
	}

	var entities []mpEntity
	if err := json.Unmarshal(body, &entities); err != nil {
		return 0, fmt.Errorf("JSON decode failed: %w", err)
	}

	count := 0
	pricesMu.Lock()
	for _, e := range entities {
		if e.Primary.Pricing.Input == nil || e.Primary.Pricing.Output == nil {
			continue
		}
		mp := modelPrice{
			Prompt:     *e.Primary.Pricing.Input,
			Completion: *e.Primary.Pricing.Output,
			AutoSynced: true,
		}
		if e.Primary.Pricing.CacheRead != nil {
			mp.Cache = *e.Primary.Pricing.CacheRead
		}
		pricesStore[e.Slug] = mp
		// Add common spelling aliases (dot↔dash, no separators)
		for _, alias := range modelVariants(e.Slug) {
			if _, exists := pricesStore[alias]; !exists {
				pricesStore[alias] = mp
			}
		}
		count++
	}
	pricesMu.Unlock()
	return count, nil
}

func handlePriceSyncImpl() pluginapi.ManagementResponse {
	count, err := syncModelPrices()
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return jsonResponse(http.StatusOK, map[string]any{"synced": count, "total": len(pricesStore)})
}

// ---------------------------------------------------------------------------
// Background auto-sync from modelprice.boxtech.icu
// ---------------------------------------------------------------------------

var (
	priceSyncOnce sync.Once
	priceSyncStop chan struct{}
	priceLastSync string
	priceSyncMu   sync.Mutex
)

func initModelPriceSync() {
	priceSyncOnce.Do(func() {
		priceSyncStop = make(chan struct{})
		go func() {
			// Immediate first sync
			go doPriceSync()
			for {
				select {
				case <-priceSyncStop:
					return
				case <-time.After(6 * time.Hour):
				}
				go doPriceSync()
			}
		}()
	})
}

func doPriceSync() {
	count, err := syncModelPrices()
	priceSyncMu.Lock()
	if err != nil {
		priceLastSync = "error: " + err.Error()
	} else {
		priceLastSync = time.Now().UTC().Format(time.RFC3339)
	}
	priceSyncMu.Unlock()
	_ = count
}

func getPriceSyncStatus() string {
	priceSyncMu.Lock()
	defer priceSyncMu.Unlock()
	if priceLastSync == "" {
		return "pending"
	}
	return priceLastSync
}
