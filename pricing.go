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
	Prices   map[string]modelPrice `json:"prices"`
	LastSync string                `json:"last_sync,omitempty"`
	SyncedAt string                `json:"synced_at,omitempty"`
}

var pricesMu sync.RWMutex
var pricesStore = make(map[string]modelPrice)

// ---------------------------------------------------------------------------
// DB persistence
// ---------------------------------------------------------------------------

// defaultPrices seeds prices only for models that the upstream price feed does
// not publish under the name they appear in usage, so a known-cheap lookup
// still resolves. Values are the provider's published price in USD per 1M
// tokens. They never override a synced or user-set entry (see loadPricesFromDB).
var defaultPrices = map[string]modelPrice{
	// Gemini 3.1 Pro is only published as "gemini-3-1-pro-preview"
	// (input $2.00 / output $12.00 / cache read $0.20 per 1M).
	"gemini-3.1-pro": {Prompt: 2.0, Completion: 12.0, Cache: 0.2},
}

func seedDefaultPrices() {
	pricesMu.Lock()
	defer pricesMu.Unlock()
	for model, price := range defaultPrices {
		if _, exists := pricesStore[model]; !exists {
			pricesStore[model] = price
		}
	}
}

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
			// Only set if not already present — auto-sync goroutine may have
			// already loaded this model from the remote API into pricesStore.
			if _, exists := pricesStore[model]; !exists {
				pricesStore[model] = mp
			}
		}
	}
	pricesMu.Unlock()
	seedDefaultPrices()
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
	payload.Price.AutoSynced = false
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
		req.Price.AutoSynced = false
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
	price, _, ok := matchPriceDetailedWithPreview(model)
	return price, ok
}

// matchPriceDetailed is like matchPrice but also reports the store key that
// was matched ("" when no price was found).
func matchPriceDetailed(model string) (modelPrice, string, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return modelPrice{}, "", false
	}
	// Exact match first
	if p, ok := pricesStore[model]; ok {
		return p, model, true
	}
	// Try lowercase
	lower := strings.ToLower(model)
	if p, ok := pricesStore[lower]; ok {
		return p, lower, true
	}
	// Generate variants (dot/dash/colon interchange, no separators)
	for _, v := range modelVariants(lower) {
		if p, ok := pricesStore[v]; ok {
			return p, v, true
		}
	}
	// Try stripping common prefixes (openai/, anthropic/, etc.)
	if idx := strings.Index(model, "/"); idx >= 0 {
		return matchPriceDetailed(model[idx+1:])
	}
	// Fall back to the base model price when the exact variant is not
	// listed, e.g. claude-opus-4-6-thinking → claude-opus-4-6,
	// gemini-3-7-flash-high → gemini-3-7-flash,
	// deepseek-v4-pro:preview → deepseek-v4-pro.
	stripped := stripVariantSuffix(lower)
	if stripped != "" && stripped != lower {
		if p, _, ok := matchPriceDetailed(stripped); ok {
			return p, stripped, true
		}
	}
	// Unify with the dashboard's normaliser: drop free/low suffixes and fix the
	// letter-digit boundary (grok4.5 → grok-4.5).
	if norm := normalizePriceModel(lower); norm != "" && norm != lower {
		if p, key, ok := matchPriceDetailed(norm); ok {
			return p, key, true
		}
	}
	return modelPrice{}, "", false
}

// matchPriceDetailedWithPreview additionally accepts a bare name against a
// published "-preview" entry (gemini-3.1-pro → gemini-3-1-pro-preview). This
// lives outside matchPriceDetailed because that function strips "-preview" as a
// variant suffix — doing it there would recurse forever.
func matchPriceDetailedWithPreview(model string) (modelPrice, string, bool) {
	// Try the raw name first, then its normalised form, so that e.g.
	// gemini-3.1-pro-low → gemini-3.1-pro → gemini-3-1-pro-preview resolves.
	seen := map[string]bool{}
	for _, candidate := range []string{model, normalizePriceModel(model)} {
		l := strings.ToLower(strings.TrimSpace(candidate))
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		if p, key, ok := matchPriceDetailed(l); ok {
			return p, key, true
		}
		if !strings.HasSuffix(l, "-preview") {
			if p, key, ok := matchPriceDetailed(l + "-preview"); ok {
				return p, key, true
			}
		}
		// Client-prefixed names refer to the same underlying model
		// (e.g. kiro-claude-opus-4-5 → claude-opus-4-5). Peel leading segments
		// and retry, so a prefix we have never seen still prices correctly.
		// Only an exact existing key can match, which keeps this bounded.
		rest := l
		for {
			idx := strings.IndexAny(rest, "-:")
			if idx <= 0 {
				break
			}
			rest = rest[idx+1:]
			if rest == "" || seen[rest] {
				break
			}
			seen[rest] = true
			if p, key, ok := matchPriceDetailed(rest); ok {
				return p, key, true
			}
			if !strings.HasSuffix(rest, "-preview") {
				if p, key, ok := matchPriceDetailed(rest + "-preview"); ok {
					return p, key, true
				}
			}
		}
	}
	return modelPrice{}, "", false
}

// suffixFallbacks lists model-name suffixes that are not priced separately;
// when present they are stripped so the base model price applies.
var suffixFallbacks = []string{
	"-thinking-pro",
	"-thinking",
	":preview",
	"-preview",
	":latest",
	"-latest",
	":exp",
	"-exp",
	"-high",
	"-ultra",
}

// stripVariantSuffix removes a trailing variant suffix (or a trailing
// version/date number like ":0813") from a model name.
func stripVariantSuffix(name string) string {
	for _, suffix := range suffixFallbacks {
		if strings.HasSuffix(name, suffix) {
			return strings.TrimSuffix(name, suffix)
		}
	}
	for _, separator := range []string{":", "-"} {
		idx := strings.LastIndex(name, separator)
		if idx > 0 && isVersionNumber(name[idx+1:]) {
			return name[:idx]
		}
	}
	return name
}

// priceVariantSuffixes are suffixes the price table does not carry separately.
// This mirrors the dashboard's normalizeModelName so the same variants the UI
// groups together also resolve to the same price key; keep the two in sync via
// testdata/model_normalization.json, which both sides assert against.
//
// Date suffixes (-0731, -2026-09-05) are deliberately NOT stripped here:
// pricing is version-specific, whereas the UI groups versions for display.
var priceVariantSuffixes = []string{"（free）", "(free)", ":free", "-free", "-low", "-thinking", "-latest", "-preview"}

// insertLetterDigitDash turns "grok4.5" into "grok-4.5", mirroring the
// dashboard normaliser so both resolve against the same price key.
func insertLetterDigitDash(s string) string {
	i := 0
	for i < len(s) && s[i] >= 'a' && s[i] <= 'z' {
		i++
	}
	if i == 0 || i >= len(s) || s[i] < '0' || s[i] > '9' {
		return s
	}
	rest := s[i:]
	for j := 0; j < len(rest); j++ {
		if c := rest[j]; !(c >= '0' && c <= '9') && c != '.' {
			return s
		}
	}
	return s[:i] + "-" + rest
}

// normalizePriceModel applies the dashboard's variant normalisation to a model
// name: strip a provider prefix, drop free/low suffixes, then fix the
// letter-digit boundary. Idempotent, so the recursive price lookup terminates.
func normalizePriceModel(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	for {
		trimmed := s
		for _, suffix := range priceVariantSuffixes {
			if strings.HasSuffix(trimmed, suffix) {
				trimmed = strings.TrimSuffix(trimmed, suffix)
				break
			}
		}
		if trimmed == s {
			break
		}
		s = trimmed
	}
	return insertLetterDigitDash(s)
}
func isVersionNumber(s string) bool {
	if len(s) < 2 || len(s) > 8 {
		return false
	}
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

// modelVariants generates common spelling variations of a model name.
func modelVariants(name string) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || s == name || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	// dot↔dash interchange
	if strings.Contains(name, ".") {
		add(strings.ReplaceAll(name, ".", "-"))
	}
	if strings.Contains(name, "-") {
		add(strings.ReplaceAll(name, "-", "."))
	}
	// colon↔dash interchange (e.g. "deepseek-v4-pro:0813" → "deepseek-v4-pro-0813")
	if strings.Contains(name, ":") {
		add(strings.ReplaceAll(name, ":", "-"))
	}
	if strings.Contains(name, "-") {
		add(strings.ReplaceAll(name, "-", ":"))
	}
	// remove all separators
	add(strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(name, "-", ""), ".", ""), ":", ""))
	// insert possible missing dashes between number-letter boundaries
	// e.g. "glm5.2" → "glm-5.2", "deepseekv4" → "deepseek-v4"
	return out
}

// ---------------------------------------------------------------------------
// Auto-sync pricing from modelprice.boxtech.icu
// ---------------------------------------------------------------------------

const modelPriceAPI = "https://modelprice.boxtech.icu/api/v2/entities"

type mpEntity struct {
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Primary struct {
		Provider string `json:"provider"`
		Pricing  struct {
			Input     *float64 `json:"input"`
			Output    *float64 `json:"output"`
			CacheRead *float64 `json:"cache_read"`
		} `json:"pricing"`
	} `json:"primary_offering"`
}

var mpClient = &http.Client{Timeout: 45 * time.Second}

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
		// Never overwrite a manually-added price (AutoSynced == false) with an auto-synced one.
		if existing, ok := pricesStore[e.Slug]; ok && !existing.AutoSynced {
			continue
		}
		pricesStore[e.Slug] = mp
		persistPrice(e.Slug, mp)
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
	count, err := doPriceSync()
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
			go func() { _, _ = doPriceSync() }()
			for {
				select {
				case <-priceSyncStop:
					return
				case <-time.After(6 * time.Hour):
				}
				go func() { _, _ = doPriceSync() }()
			}
		}()
	})
}

// priceSyncRunMu ensures only one sync runs at a time. Starting the plugin
// fires an immediate sync while a manual "sync now" click or the 6h ticker can
// race it; two concurrent 700KB downloads made the upstream time out.
var priceSyncRunMu sync.Mutex

func doPriceSync() (int, error) {
	if !priceSyncRunMu.TryLock() {
		return 0, fmt.Errorf("a price sync is already running")
	}
	defer priceSyncRunMu.Unlock()

	count, err := syncModelPricesWithRetry()
	priceSyncMu.Lock()
	if err != nil {
		priceLastSync = "error: " + err.Error()
	} else {
		priceLastSync = time.Now().UTC().Format(time.RFC3339)
	}
	priceSyncMu.Unlock()
	if err != nil {
		recordError("price_sync_failed", err.Error())
	}
	return count, err
}

// syncModelPricesWithRetry gives the upstream one retry: it occasionally fails
// to send response headers within the client timeout.
func syncModelPricesWithRetry() (int, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		count, err := syncModelPrices()
		if err == nil {
			return count, nil
		}
		lastErr = err
		if attempt == 0 {
			time.Sleep(3 * time.Second)
		}
	}
	return 0, lastErr
}

func getPriceSyncStatus() string {
	priceSyncMu.Lock()
	defer priceSyncMu.Unlock()
	if priceLastSync == "" {
		return "pending"
	}
	return priceLastSync
}

// countUnpricedModels reports how many distinct models seen in usage have no
// price entry, so an all-zero cost dashboard becomes visible instead of silent.
func countUnpricedModels() int64 {
	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil {
		return 0
	}
	// Only models that actually consumed tokens are relevant: a model seen
	// solely in zero-token requests (e.g. failed probes) costs $0 whether or
	// not it is priced, so counting it would raise a permanent false alarm.
	rows, err := d.Query(`SELECT model FROM usage_events WHERE model != '' GROUP BY model
	                      HAVING COALESCE(SUM(total_tokens),0) > 0
	                          OR COALESCE(SUM(input_tokens),0) > 0
	                          OR COALESCE(SUM(output_tokens),0) > 0`)
	if err != nil {
		return 0
	}
	defer rows.Close()

	var missing int64
	for rows.Next() {
		var model string
		if rows.Scan(&model) != nil {
			continue
		}
		if _, ok := matchPrice(model); !ok {
			missing++
		}
	}
	return missing
}
