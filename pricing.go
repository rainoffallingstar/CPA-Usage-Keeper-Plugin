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

func init() {
	pricesMu.Lock()
	defer pricesMu.Unlock()
	pricesStore = make(map[string]modelPrice)
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
	case "delete":
		if model == "" {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "model is required"})
		}
		pricesMu.Lock()
		delete(pricesStore, model)
		pricesMu.Unlock()
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
	case "delete":
		pricesMu.Lock()
		delete(pricesStore, model)
		pricesMu.Unlock()
	default:
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "unknown action, use 'set' or 'delete'"})
	}
	return handleGetPrices()
}

func computeCost(model string, inputTokens, outputTokens, cachedTokens int64) float64 {
	pricesMu.RLock()
	price, ok := pricesStore[model]
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
		}
		if e.Primary.Pricing.CacheRead != nil {
			mp.Cache = *e.Primary.Pricing.CacheRead
		}
		pricesStore[e.Slug] = mp
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
