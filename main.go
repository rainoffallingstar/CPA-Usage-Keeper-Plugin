package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	void* call;
	void* free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// Global state
// ---------------------------------------------------------------------------

var (
	activeConfig atomic.Value
	db           *sql.DB
	dbMu         sync.RWMutex
	initOnce     sync.Once
	shutdownOnce sync.Once

	// In-memory ring buffer for recent events (serves dashboard instantly).
	ringBuf   []usageEvent
	ringHead  int
	ringCount int
	ringMu    sync.RWMutex
)

func init() {
	activeConfig.Store(defaultConfig())
}

func main() {}

// ---------------------------------------------------------------------------
// C ABI exports
// ---------------------------------------------------------------------------

//export cliproxy_plugin_init
func cliproxy_plugin_init(_ *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}

	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}

	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = len
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	shutdownOnce.Do(func() {
		dbMu.Lock()
		if db != nil {
			_ = db.Close()
			db = nil
		}
		dbMu.Unlock()
	})
}

// ---------------------------------------------------------------------------
// RPC method dispatch
// ---------------------------------------------------------------------------

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if errConfigure := configure(request); errConfigure != nil {
			return nil, errConfigure
		}
		ensureDB()
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodUsageHandle:
		return handleUsage(request)
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegResponse())
	case pluginabi.MethodManagementHandle:
		return handleManagement(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

func configure(raw []byte) error {
	var req lifecycleRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return errUnmarshal
		}
	}

	cfg := defaultConfig()
	if len(req.ConfigYAML) > 0 {
		values := usageStatisticsConfigValues(req.ConfigYAML)
		var decoded pluginConfig
		// Try direct unmarshal
		if errUnmarshal := yaml.Unmarshal(req.ConfigYAML, &decoded); errUnmarshal != nil {
			return errUnmarshal
		}
		// Override from nested config paths
		if v, ok := stringConfig(values, "db_path"); ok {
			decoded.DBPath = v
		}
		if v, ok := intConfig(values, "retention_days"); ok {
			decoded.RetentionDays = v
		}
		if v, ok := intConfig(values, "max_in_memory_events"); ok {
			decoded.MaxInMemoryEvents = v
		}
		if v, ok := intConfig(values, "refresh_seconds"); ok {
			decoded.RefreshSeconds = v
		}
		if v, ok := stringConfig(values, "api_key_hash_salt"); ok {
			decoded.APIKeyHashSalt = v
		}
		cfg = mergeConfig(cfg, decoded)
	}
	activeConfig.Store(normalizeConfig(cfg))
	return nil
}

func usageStatisticsConfigValues(yamlBytes []byte) map[string]interface{} {
	var root map[string]interface{}
	if err := yaml.Unmarshal(yamlBytes, &root); err != nil {
		return nil
	}
	// Try multiple fallback paths
	if values, ok := nestedMap(root, "plugins", "configs", "usage-keeper"); ok {
		return values
	}
	if values, ok := nestedMap(root, "configs", "usage-keeper"); ok {
		return values
	}
	if values, ok := nestedMap(root, "usage-keeper"); ok {
		return values
	}
	return root
}

func nestedMap(root map[string]interface{}, path ...string) (map[string]interface{}, bool) {
	current := root
	for _, key := range path {
		value, ok := current[key]
		if !ok {
			return nil, false
		}
		next, ok := value.(map[string]interface{})
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

func intConfig(values map[string]interface{}, key string) (int, bool) {
	val, ok := values[key]
	if !ok {
		return 0, false
	}
	switch v := val.(type) {
	case int:
		if v >= 0 {
			return v, true
		}
	case int64:
		if v >= 0 {
			return int(v), true
		}
	case float64:
		if v >= 0 && v == float64(int(v)) {
			return int(v), true
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
			return n, true
		}
	}
	return 0, false
}

func stringConfig(values map[string]interface{}, key string) (string, bool) {
	val, ok := values[key]
	if !ok {
		return "", false
	}
	return strings.TrimSpace(fmt.Sprint(val)), true
}

func mergeConfig(base, override pluginConfig) pluginConfig {
	if strings.TrimSpace(override.DBPath) != "" {
		base.DBPath = override.DBPath
	}
	if override.RetentionDays > 0 {
		base.RetentionDays = override.RetentionDays
	}
	if override.MaxInMemoryEvents > 0 {
		base.RefreshSeconds = override.RefreshSeconds
		base.MaxInMemoryEvents = override.MaxInMemoryEvents
	}
	return base
}

func normalizeConfig(cfg pluginConfig) pluginConfig {
	cfg.DBPath = strings.TrimSpace(cfg.DBPath)
	if cfg.DBPath == "" {
		cfg.DBPath = defaultDBPath
	}
	if cfg.RetentionDays <= 0 {
		cfg.RetentionDays = defaultRetentionDays
	}
	if cfg.MaxInMemoryEvents <= 0 {
		cfg.MaxInMemoryEvents = defaultMaxInMemoryEvents
	}
	if cfg.MaxInMemoryEvents > 10000 {
		cfg.MaxInMemoryEvents = 10000
	}
	if cfg.RefreshSeconds < 0 {
		cfg.RefreshSeconds = defaultRefreshSeconds
	} else if cfg.RefreshSeconds > 3600 {
		cfg.RefreshSeconds = defaultRefreshSeconds
	}
	return cfg
}

func currentConfig() pluginConfig {
	raw := activeConfig.Load()
	if cfg, ok := raw.(pluginConfig); ok {
		return cfg
	}
	return defaultConfig()
}

// ---------------------------------------------------------------------------
// Plugin registration
// ---------------------------------------------------------------------------

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "Usage Keeper",
			Version:          pluginVersion,
			Author:           "router-for-me",
			GitHubRepository: "https://github.com/router-for-me/cpa-plugin-usage-keeper",
			ConfigFields: []pluginapi.ConfigField{
				{
					Name:        "db_path",
					Type:        pluginapi.ConfigFieldTypeString,
					Description: "Path to the SQLite database file for persistent usage storage.",
				},
				{
					Name:        "retention_days",
					Type:        pluginapi.ConfigFieldTypeInteger,
					Description: "Number of days to retain usage records before automatic cleanup.",
				},
				{
					Name:        "max_in_memory_events",
					Type:        pluginapi.ConfigFieldTypeInteger,
					Description: "Maximum number of recent events kept in memory for fast dashboard rendering.",
				},
				{
					Name:        "refresh_seconds",
					Type:        pluginapi.ConfigFieldTypeInteger,
					Description: "Dashboard auto-refresh interval in seconds. 0 disables auto-refresh. Max 3600.",
				},
			},
		},
		Capabilities: registrationCapabilities{
			UsagePlugin:   true,
			ManagementAPI: true,
		},
	}
}

// ---------------------------------------------------------------------------
// Database initialization
// ---------------------------------------------------------------------------

func ensureDB() {
	initOnce.Do(func() {
		cfg := currentConfig()
		var err error
		db, err = sql.Open("sqlite", cfg.DBPath+"?_journal_mode=WAL&_busy_timeout=5000")
		if err != nil {
			panic(fmt.Sprintf("usage-keeper: failed to open database: %v", err))
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)

		if errCreate := createTables(); errCreate != nil {
			panic(fmt.Sprintf("usage-keeper: failed to create tables: %v", errCreate))
		}

		// Migration: add cache detail columns for existing databases
		_, _ = db.Exec("ALTER TABLE usage_events ADD COLUMN cache_read_tokens INTEGER NOT NULL DEFAULT 0")
		_, _ = db.Exec("ALTER TABLE usage_events ADD COLUMN cache_creation_tokens INTEGER NOT NULL DEFAULT 0")

		// Load recent events into ring buffer
		loadRecentIntoRing()
	})
}

func createTables() error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS usage_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp DATETIME NOT NULL DEFAULT (datetime('now')),
			provider TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			alias TEXT NOT NULL DEFAULT '',
			auth_id TEXT NOT NULL DEFAULT '',
			auth_type TEXT NOT NULL DEFAULT '',
			auth_index TEXT NOT NULL DEFAULT '',
			api_key TEXT NOT NULL DEFAULT '',
			hashed_api_key TEXT NOT NULL DEFAULT '',
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			reasoning_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			cached_tokens INTEGER NOT NULL DEFAULT 0,
			cache_read_tokens INTEGER NOT NULL DEFAULT 0,
			cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
			latency_ms INTEGER NOT NULL DEFAULT 0,
			ttft_ms INTEGER NOT NULL DEFAULT 0,
			failed INTEGER NOT NULL DEFAULT 0,
			failure_status_code INTEGER NOT NULL DEFAULT 0,
			failure_body TEXT NOT NULL DEFAULT '',
			executor_type TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			service_tier TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS idx_usage_events_timestamp ON usage_events(timestamp);
		CREATE INDEX IF NOT EXISTS idx_usage_events_model ON usage_events(model);
		CREATE INDEX IF NOT EXISTS idx_usage_events_provider ON usage_events(provider);
		CREATE INDEX IF NOT EXISTS idx_usage_events_failed ON usage_events(failed);
	`)
	// Schema migration: add hashed_api_key for existing databases
	_, _ = db.Exec(`ALTER TABLE usage_events ADD COLUMN hashed_api_key TEXT NOT NULL DEFAULT ''`)
	return err
}

func loadRecentIntoRing() {
	cfg := currentConfig()
	ringBuf = make([]usageEvent, cfg.MaxInMemoryEvents)
	ringHead = 0
	ringCount = 0

	rows, err := db.Query(
		"SELECT id, timestamp, provider, model, input_tokens, output_tokens, total_tokens, latency_ms, failed, failure_body, auth_id, executor_type, cached_tokens FROM usage_events ORDER BY id DESC LIMIT ?",
		cfg.MaxInMemoryEvents,
	)
	if err != nil {
		return
	}
	defer rows.Close()

	var events []usageEvent
	for rows.Next() {
		var e usageEvent
		var cached int64
		if errScan := rows.Scan(&e.ID, &e.Timestamp, &e.Provider, &e.Model, &e.InputTokens, &e.OutputTokens, &e.TotalTokens, &e.LatencyMs, &e.Failed, &e.FailureBody, &e.AuthID, &e.ExecutorType, &cached); errScan != nil {
			continue
		}
		e.CacheHitRate = cacheHitRate(cached, e.InputTokens)
		events = append(events, e)
	}

	ringMu.Lock()
	for i := len(events) - 1; i >= 0; i-- {
		ringBuf[ringHead] = events[i]
		ringHead = (ringHead + 1) % len(ringBuf)
		ringCount++
		if ringCount > len(ringBuf) {
			ringCount = len(ringBuf)
		}
	}
	ringMu.Unlock()
}

// ---------------------------------------------------------------------------
// Usage event handling
// ---------------------------------------------------------------------------

func handleUsage(raw []byte) ([]byte, error) {
	ensureDB()

	var record pluginapi.UsageRecord
	if errUnmarshal := json.Unmarshal(raw, &record); errUnmarshal != nil {
		return okEnvelopeJSON("{}")
	}

	// Build the usage event
	event := usageEvent{
		Timestamp:    record.RequestedAt.Format(time.RFC3339),
		Provider:     record.Provider,
		Model:        record.Model,
		InputTokens:  record.Detail.InputTokens,
		OutputTokens: record.Detail.OutputTokens,
		TotalTokens:  record.Detail.TotalTokens,
		LatencyMs:    record.Latency.Milliseconds(),
		Failed:       record.Failed,
		CacheHitRate: cacheHitRate(record.Detail.CachedTokens, record.Detail.InputTokens),
		AuthID:       record.AuthID,
		ExecutorType: record.ExecutorType,
	}
	if record.Failed {
		event.FailureBody = record.Failure.Body
	}

	// Hash API key for privacy before storing
	hashedKey := ""
	maskedKey := record.APIKey
	if record.APIKey != "" {
		if looksLikeSecretKey(record.APIKey) {
			maskedKey = maskAPIKey(record.APIKey)
		}
		hashedKey = hashAPIKey(record.APIKey)
	}

	// Insert into SQLite
	result, errInsert := db.Exec(
		`INSERT INTO usage_events (timestamp, provider, model, alias, auth_id, auth_type, auth_index, api_key, hashed_api_key,
		 input_tokens, output_tokens, reasoning_tokens, total_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens,
		 latency_ms, ttft_ms, failed, failure_status_code, failure_body,
		 executor_type, source, service_tier)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.RequestedAt.Format(time.RFC3339),
		record.Provider,
		record.Model,
		record.Alias,
		record.AuthID,
		record.AuthType,
		record.AuthIndex,
		maskedKey,
		hashedKey,
		record.Detail.InputTokens,
		record.Detail.OutputTokens,
		record.Detail.ReasoningTokens,
		record.Detail.TotalTokens,
		record.Detail.CachedTokens,
		record.Detail.CacheReadTokens,
		record.Detail.CacheCreationTokens,
		record.Latency.Milliseconds(),
		record.TTFT.Milliseconds(),
		boolToInt(record.Failed),
		record.Failure.StatusCode,
		record.Failure.Body,
		record.ExecutorType,
		record.Source,
		record.ServiceTier,
	)
	if errInsert != nil {
		return okEnvelopeJSON("{}")
	}

	lastID, _ := result.LastInsertId()
	event.ID = lastID

	// Add to ring buffer
	cfg := currentConfig()
	ringMu.Lock()
	ringBuf[ringHead] = event
	ringHead = (ringHead + 1) % len(ringBuf)
	ringCount++
	if ringCount > len(ringBuf) {
		ringCount = len(ringBuf)
	}
	ringMu.Unlock()

	// Periodically clean old records (probabilistic to avoid overhead)
	if ringCount%100 == 0 {
		go cleanupOldRecords(cfg.RetentionDays)
	}

	return okEnvelopeJSON("{}")
}

func cleanupOldRecords(retentionDays int) {
	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays).Format(time.RFC3339)
	_, _ = d.Exec("DELETE FROM usage_events WHERE timestamp < ?", cutoff)
}

// ---------------------------------------------------------------------------
// Management API registration
// ---------------------------------------------------------------------------

func managementRegResponse() managementRegistrationResponse {
	return managementRegistrationResponse{
		Routes: []pluginapi.ManagementRoute{
			{Method: http.MethodGet, Path: "/usage-keeper/summary"},
			{Method: http.MethodGet, Path: "/usage-keeper/models"},
			{Method: http.MethodGet, Path: "/usage-keeper/events"},
			{Method: http.MethodPost, Path: "/usage-keeper/cleanup"},
			{Method: http.MethodGet, Path: "/usage"},
			{Method: http.MethodGet, Path: "/usage-keeper/health"},
			{Method: http.MethodGet, Path: "/usage-keeper/prices"},
			{Method: http.MethodPut, Path: "/usage-keeper/prices"},
			{Method: http.MethodDelete, Path: "/usage-keeper/prices"},
			{Method: http.MethodGet, Path: "/usage-keeper/export"},
			{Method: http.MethodPost, Path: "/usage-keeper/import"},
		},
		Resources: []pluginapi.ResourceRoute{
			{
				Path:        "/dashboard",
				Menu:        "Usage Keeper",
				Description: "View CPA usage statistics, model breakdowns, and request history.",
			},
			{
				Path:        "/api/summary",
				Menu:        "",
				Description: "Usage summary JSON API.",
			},
			{
				Path:        "/api/models",
				Menu:        "",
				Description: "Model breakdown JSON API.",
			},
			{
				Path:        "/api/events",
				Menu:        "",
				Description: "Usage events JSON API.",
			},
			{
				Path:        "/api/usage",
				Menu:        "",
				Description: "Quotio-compatible aggregate usage JSON API.",
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Management request dispatch
// ---------------------------------------------------------------------------

func handleManagement(raw []byte) ([]byte, error) {
	ensureDB()

	var req managementRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}

	path := strings.TrimRight(strings.TrimSpace(req.Path), "/")
	if path == "" {
		path = "/"
	}

	switch {
	case strings.EqualFold(req.Method, http.MethodGet) && path == resourceDashboardPath:
		return okEnvelope(htmlResponse(http.StatusOK, renderDashboard()))
	case strings.EqualFold(req.Method, http.MethodGet) && (path == resourceAPISummaryPath || path == managementSummaryPath || path == managementUsageCompatPath || path == resourceAPIUsagePath):
		return okEnvelope(handleSummary(req.Query, req.Headers))
	case strings.EqualFold(req.Method, http.MethodGet) && (path == resourceAPIModelsPath || path == managementModelsPath):
		return okEnvelope(handleModels(req.Query))
	case strings.EqualFold(req.Method, http.MethodGet) && (path == resourceAPIEventsPath || path == managementEventsPath):
		return okEnvelope(handleEvents(req.Query, req.Headers))
	case strings.EqualFold(req.Method, http.MethodPost) && path == managementCleanupPath:
		return okEnvelope(handleCleanup())
	case strings.EqualFold(req.Method, http.MethodGet) && strings.HasSuffix(path, "/health"):
		return okEnvelope(handleHealthCheck())
	case strings.EqualFold(req.Method, http.MethodGet) && strings.HasSuffix(path, "/prices"):
		return okEnvelope(handleGetPrices())
	case strings.EqualFold(req.Method, http.MethodPut) && strings.HasSuffix(path, "/prices"):
		return okEnvelope(handlePutPrice(req.Body))
	case strings.EqualFold(req.Method, http.MethodDelete) && strings.HasSuffix(path, "/prices"):
		return okEnvelope(handleDeletePrice(req.Query))
	case strings.EqualFold(req.Method, http.MethodGet) && strings.HasSuffix(path, "/export"):
		return okEnvelope(handleExportUsage())
	case strings.EqualFold(req.Method, http.MethodPost) && strings.HasSuffix(path, "/export-jobs"):
		return okEnvelope(handleCreateExportJob(req.Query))
	case strings.EqualFold(req.Method, http.MethodGet) && strings.HasSuffix(path, "/export-jobs"):
		return okEnvelope(handleGetExportJobs(req.Query))
	case strings.EqualFold(req.Method, http.MethodGet) && strings.HasSuffix(path, "/export-download"):
		return okEnvelope(handleGetExportDownload(req.Query))
	case strings.EqualFold(req.Method, http.MethodDelete) && strings.HasSuffix(path, "/export-jobs"):
		return okEnvelope(handleDeleteExportJob(req.Query))
	case strings.EqualFold(req.Method, http.MethodPost) && strings.HasSuffix(path, "/import"):
		return okEnvelope(handleImportUsage(req.Body))
	default:
		return okEnvelope(jsonResponse(http.StatusNotFound, map[string]any{"error": "route not found"}))
	}
}

// ---------------------------------------------------------------------------
// API handlers
// ---------------------------------------------------------------------------

func parseRangeHours(query map[string][]string) int {
	rangeStr := ""
	if vals, ok := query["range"]; ok && len(vals) > 0 {
		rangeStr = strings.TrimSpace(vals[0])
	}
	switch strings.ToLower(rangeStr) {
	case "1h":
		return 1
	case "6h":
		return 6
	case "24h", "day":
		return 24
	case "7d", "week":
		return 7 * 24
	case "30d", "month":
		return 30 * 24
	default:
		return 24
	}
}

func handleSummary(query map[string][]string, headers map[string][]string) pluginapi.ManagementResponse {
	// Detect Quotio caller: no range param means return the Quotio shape
	_, hasRange := query["range"]
	if !hasRange && len(query) == 0 {
		return handleQuotioUsage()
	}

	rangeHours := parseRangeHours(query)

	// Generate ETag for conditional caching
	etag := dashboardWeakETag("summary", fmt.Sprintf("%d", rangeHours))
	if checkETag(headers, etag) {
		cacheMu.Lock()
		summaryCacheHits++
		cacheMu.Unlock()
		return notModifiedResponse(etag)
	}
	cacheMu.Lock()
	summaryCacheMisses++
	cacheMu.Unlock()

	dbMu.RLock()
	d := db
	dbMu.RUnlock()

	var resp summaryResponse
	var cacheReadTotal int64
	resp.RangeHours = rangeHours

	since := time.Now().Add(-time.Duration(rangeHours) * time.Hour).Format(time.RFC3339)

	if d != nil {
		_ = d.QueryRow(
			"SELECT COUNT(*), COALESCE(SUM(total_tokens),0), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(failed),0), COUNT(DISTINCT model), COALESCE(AVG(latency_ms),0), COALESCE(SUM(cached_tokens),0) FROM usage_events WHERE timestamp >= ?",
			since,
		).Scan(&resp.TotalRequests, &resp.TotalTokens, &resp.InputTokens, &resp.OutputTokens, &resp.FailedRequests, &resp.UniqueModels, &resp.AvgLatencyMs, &cacheReadTotal)
		if resp.InputTokens > 0 {
			resp.CacheHitRate = float64(cacheReadTotal) / float64(resp.InputTokens) * 100
		}
	}

	return jsonResponse(http.StatusOK, resp)
}

// handleQuotioUsage returns the aggregate shape expected by Quotio (UsageStats).

func handleQuotioUsage() pluginapi.ManagementResponse {
	dbMu.RLock()
	d := db
	dbMu.RUnlock()

	if d == nil {
		return jsonResponse(http.StatusOK, quotioUsageResponse{})
	}

	// Use 24h window by default
	since := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	var total, failed, totalToks, inputToks, outputToks int64
	_ = d.QueryRow(
		"SELECT COUNT(*), COALESCE(SUM(failed),0), COALESCE(SUM(total_tokens),0), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0) FROM usage_events WHERE timestamp >= ?",
		since,
	).Scan(&total, &failed, &totalToks, &inputToks, &outputToks)

	resp := quotioUsageResponse{
		Usage: quotioUsageData{
			TotalRequests: total,
			SuccessCount:  total - failed,
			FailureCount:  failed,
			TotalTokens:   totalToks,
			InputTokens:   inputToks,
			OutputTokens:  outputToks,
		},
		FailedReqs: failed,
	}
	return jsonResponse(http.StatusOK, resp)
}

func handleModels(query map[string][]string) pluginapi.ManagementResponse {
	rangeHours := parseRangeHours(query)
	provider := ""
	if vals, ok := query["provider"]; ok && len(vals) > 0 {
		provider = strings.TrimSpace(vals[0])
	}

	dbMu.RLock()
	d := db
	dbMu.RUnlock()

	since := time.Now().Add(-time.Duration(rangeHours) * time.Hour).Format(time.RFC3339)
	models := make([]modelBreakdown, 0)

	if d != nil {
		var rows *sql.Rows
		var err error
		if provider != "" {
			rows, err = d.Query(
				"SELECT provider, model, COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(total_tokens),0), COALESCE(SUM(cached_tokens),0) FROM usage_events WHERE timestamp >= ? AND provider = ? GROUP BY provider, model ORDER BY SUM(total_tokens) DESC",
				since, provider,
			)
		} else {
			rows, err = d.Query(
				"SELECT COALESCE(NULLIF(GROUP_CONCAT(DISTINCT provider),\"\"),\"multiple\") as provider, model, COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(total_tokens),0), COALESCE(SUM(cached_tokens),0) FROM usage_events WHERE timestamp >= ? GROUP BY model ORDER BY SUM(total_tokens) DESC",
				since,
			)
		}
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var m modelBreakdown
				if errScan := rows.Scan(&m.Provider, &m.Model, &m.Requests, &m.InputTokens, &m.OutputTokens, &m.TotalTokens, &m.CachedTokens); errScan == nil {
					m.Cost = computeCost(m.Model, m.InputTokens, m.OutputTokens, m.CachedTokens)
					models = append(models, m)
				}
			}
		}
	}

	return jsonResponse(http.StatusOK, models)
}

func handleEvents(query map[string][]string, headers map[string][]string) pluginapi.ManagementResponse {
	limit := 50
	offset := 0
	rangeHours := 24

	if vals, ok := query["limit"]; ok && len(vals) > 0 {
		if n, errParse := parseInt(vals[0]); errParse == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	if vals, ok := query["offset"]; ok && len(vals) > 0 {
		if n, errParse := parseInt(vals[0]); errParse == nil && n >= 0 {
			offset = n
		}
	}
	rangeHours = parseRangeHours(query)

	// Multi-dimensional filters
	modelFilter := ""
	if vals, ok := query["model"]; ok && len(vals) > 0 {
		modelFilter = strings.TrimSpace(vals[0])
	}
	sourceFilter := ""
	if vals, ok := query["source"]; ok && len(vals) > 0 {
		sourceFilter = strings.TrimSpace(vals[0])
	}
	authFilter := ""
	if vals, ok := query["auth"]; ok && len(vals) > 0 {
		authFilter = strings.TrimSpace(vals[0])
	}

	// ETag conditional caching
	etag := dashboardWeakETag("events", fmt.Sprintf("%d-%d-%d-%s-%s-%s", limit, offset, rangeHours, modelFilter, sourceFilter, authFilter))
	if checkETag(headers, etag) {
		cacheMu.Lock()
		eventsCacheHits++
		cacheMu.Unlock()
		return notModifiedResponse(etag)
	}
	cacheMu.Lock()
	eventsCacheMisses++
	cacheMu.Unlock()

	since := time.Now().Add(-time.Duration(rangeHours) * time.Hour).Format(time.RFC3339)

	var resp eventsResponse
	resp.Limit = limit
	resp.Offset = offset

	dbMu.RLock()
	d := db
	dbMu.RUnlock()

	if d != nil {
		// Build dynamic query with filters
		where := "WHERE timestamp >= ?"
		args := []interface{}{since}
		if modelFilter != "" {
			where += " AND model = ?"
			args = append(args, modelFilter)
		}
		if sourceFilter != "" {
			where += " AND source = ?"
			args = append(args, sourceFilter)
		}
		if authFilter != "" {
			where += " AND auth_id = ?"
			args = append(args, authFilter)
		}

		countArgs := append([]interface{}{}, args...)
		_ = d.QueryRow("SELECT COUNT(*) FROM usage_events "+where, countArgs...).Scan(&resp.Total)

		queryArgs := append(args, limit, offset)
		rows, err := d.Query(
			"SELECT id, timestamp, provider, model, input_tokens, output_tokens, total_tokens, latency_ms, failed, failure_body, auth_id, executor_type, cached_tokens FROM usage_events "+where+" ORDER BY id DESC LIMIT ? OFFSET ?",
			queryArgs...,
		)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var e usageEvent
				var cachedEvt int64
				if errScan := rows.Scan(&e.ID, &e.Timestamp, &e.Provider, &e.Model, &e.InputTokens, &e.OutputTokens, &e.TotalTokens, &e.LatencyMs, &e.Failed, &e.FailureBody, &e.AuthID, &e.ExecutorType, &cachedEvt); errScan == nil {
					e.CacheHitRate = cacheHitRate(cachedEvt, e.InputTokens)
					resp.Events = append(resp.Events, e)
				}
			}
		}
	}

	return jsonResponse(http.StatusOK, resp)
}

func handleCleanup() pluginapi.ManagementResponse {
	cfg := currentConfig()
	cleanupOldRecords(cfg.RetentionDays)
	return jsonResponse(http.StatusOK, map[string]any{"ok": true, "message": "cleanup triggered"})
}

// ---------------------------------------------------------------------------
// Export usage data
// ---------------------------------------------------------------------------

func handleExportUsage() pluginapi.ManagementResponse {
	dbMu.RLock()
	d := db
	dbMu.RUnlock()

	if d == nil {
		return jsonResponse(http.StatusOK, map[string]any{"events": []usageEvent{}, "total": 0})
	}

	rows, err := d.Query("SELECT id, timestamp, provider, model, input_tokens, output_tokens, total_tokens, latency_ms, failed, failure_body, auth_id, executor_type, cached_tokens FROM usage_events ORDER BY id ASC LIMIT 100000")
	if err != nil {
		return jsonResponse(http.StatusInternalServerError, map[string]string{"error": "query failed"})
	}
	defer rows.Close()

	events := make([]usageEvent, 0)
	for rows.Next() {
		var e usageEvent
		var cachedEvt int64
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.Provider, &e.Model, &e.InputTokens, &e.OutputTokens, &e.TotalTokens, &e.LatencyMs, &e.Failed, &e.FailureBody, &e.AuthID, &e.ExecutorType, &cachedEvt); err == nil {
			e.CacheHitRate = cacheHitRate(cachedEvt, e.InputTokens)
			events = append(events, e)
		}
	}

	return jsonResponse(http.StatusOK, map[string]any{"version": 1, "exported_at": time.Now().UTC().Format(time.RFC3339), "events": events, "total": len(events)})
}

// ---------------------------------------------------------------------------
// Import usage data
// ---------------------------------------------------------------------------

func handleImportUsage(body []byte) pluginapi.ManagementResponse {
	const maxBodySize = 50 * 1024 * 1024

	if len(body) > maxBodySize {
		return jsonResponse(http.StatusRequestEntityTooLarge, map[string]string{"error": "payload too large"})
	}

	var payload struct {
		Events []struct {
			Timestamp    string `json:"timestamp"`
			Provider     string `json:"provider"`
			Model        string `json:"model"`
			InputTokens  int64  `json:"input_tokens"`
			OutputTokens int64  `json:"output_tokens"`
			TotalTokens  int64  `json:"total_tokens"`
			LatencyMs    int64  `json:"latency_ms"`
			Failed       bool   `json:"failed"`
			FailureBody  string `json:"failure_body,omitempty"`
			AuthID       string `json:"auth_id"`
			ExecutorType string `json:"executor_type"`
			CacheTokens  int64  `json:"cached_tokens"`
		} `json:"events"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
	}

	if len(payload.Events) > 200000 {
		return jsonResponse(http.StatusRequestEntityTooLarge, map[string]string{"error": "too many records"})
	}

	ensureDB()
	dbMu.Lock()
	d := db
	dbMu.Unlock()

	added := 0
	skipped := 0

	for _, e := range payload.Events {
		if e.Timestamp == "" {
			e.Timestamp = time.Now().Format(time.RFC3339)
		}
		failedInt := 0
		if e.Failed {
			failedInt = 1
		}
		_, err := d.Exec(
			`INSERT INTO usage_events (timestamp, provider, model, alias, auth_id, auth_type, auth_index, api_key, hashed_api_key,
			 input_tokens, output_tokens, reasoning_tokens, total_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens,
			 latency_ms, ttft_ms, failed, failure_status_code, failure_body,
			 executor_type, source, service_tier)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.Timestamp, e.Provider, e.Model, "", e.AuthID, "", "", "", "",
			e.InputTokens, e.OutputTokens, 0, e.TotalTokens, e.CacheTokens, 0, 0,
			e.LatencyMs, 0, failedInt, 0, e.FailureBody,
			e.ExecutorType, "", "default",
		)
		if err == nil {
			added++
		} else {
			skipped++
		}
	}

	return jsonResponse(http.StatusOK, map[string]any{"added": added, "skipped": skipped, "total": added + skipped})
}

// ---------------------------------------------------------------------------
// ---------------------------------------------------------------------------
// Embedded dashboard HTML
// ---------------------------------------------------------------------------

func renderDashboard() string {
	cfg := currentConfig()
	return fmt.Sprintf(dashboardHTML, cfg.RetentionDays, cfg.RefreshSeconds*1000, cfg.RefreshSeconds, cfg.RefreshSeconds)
}

// ---------------------------------------------------------------------------
// Helper functions
// ---------------------------------------------------------------------------
// cacheHitRate returns the cache hit rate as a percentage, or 0 if input tokens is zero.
func cacheHitRate(cacheRead, inputTokens int64) float64 {
	if inputTokens <= 0 {
		return 0
	}
	return float64(cacheRead) / float64(inputTokens) * 100
}

func okEnvelope(result any) ([]byte, error) {
	raw, errMarshal := json.Marshal(result)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: json.RawMessage(raw)})
}

func okEnvelopeJSON(result string) ([]byte, error) {
	return json.Marshal(envelope{OK: true, Result: json.RawMessage(result)})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

func jsonResponse(statusCode int, body any) pluginapi.ManagementResponse {
	raw, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		return pluginapi.ManagementResponse{
			StatusCode: http.StatusInternalServerError,
			Headers:    http.Header{"Content-Type": {contentTypeJSON}},
			Body:       []byte(`{"error":"marshal failed"}`),
		}
	}
	return pluginapi.ManagementResponse{
		StatusCode: statusCode,
		Headers:    http.Header{"Content-Type": {contentTypeJSON}},
		Body:       raw,
	}
}

func htmlResponse(statusCode int, body string) pluginapi.ManagementResponse {
	return pluginapi.ManagementResponse{
		StatusCode: statusCode,
		Headers:    http.Header{"Content-Type": {contentTypeHTML}},
		Body:       []byte(body),
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func parseInt(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n)
	return n, err
}
