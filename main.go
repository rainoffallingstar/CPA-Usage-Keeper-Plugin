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
// Constants
// ---------------------------------------------------------------------------

const (
	pluginID                   = "usage-keeper"
	defaultDBPath              = "usage-keeper.db"
	defaultRetentionDays       = 90
	defaultRefreshSeconds      = 0
	defaultMaxInMemoryEvents   = 1000
	contentTypeJSON            = "application/json; charset=utf-8"
	contentTypeHTML            = "text/html; charset=utf-8"
	managementSummaryPath      = "/v0/management/usage-keeper/summary"
	managementModelsPath       = "/v0/management/usage-keeper/models"
	managementEventsPath       = "/v0/management/usage-keeper/events"
	managementCleanupPath      = "/v0/management/usage-keeper/cleanup"
	resourceDashboardPath      = "/v0/resource/plugins/usage-keeper/dashboard"
	resourceAPISummaryPath     = "/v0/resource/plugins/usage-keeper/api/summary"
	resourceAPIModelsPath      = "/v0/resource/plugins/usage-keeper/api/models"
	resourceAPIEventsPath      = "/v0/resource/plugins/usage-keeper/api/events"
	managementUsageCompatPath  = "/v0/management/usage"
	resourceAPIUsagePath       = "/v0/resource/plugins/usage-keeper/api/usage"
)

var pluginVersion = "0.1.0"

// ---------------------------------------------------------------------------
// Plugin configuration
// ---------------------------------------------------------------------------

type pluginConfig struct {
	DBPath            string `yaml:"db_path"`
	RetentionDays     int    `yaml:"retention_days"`
	MaxInMemoryEvents int    `yaml:"max_in_memory_events"`
	RefreshSeconds    int    `yaml:"refresh_seconds"`
}

func defaultConfig() pluginConfig {
	return pluginConfig{
		DBPath:            defaultDBPath,
		RetentionDays:     defaultRetentionDays,
		MaxInMemoryEvents: defaultMaxInMemoryEvents,
		RefreshSeconds:    defaultRefreshSeconds,
	}
}

// ---------------------------------------------------------------------------
// RPC envelope types
// ---------------------------------------------------------------------------

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type registrationCapabilities struct {
	UsagePlugin   bool `json:"usage_plugin"`
	ManagementAPI bool `json:"management_api"`
}

type managementRegistrationResponse struct {
	Routes    []pluginapi.ManagementRoute `json:"routes,omitempty"`
	Resources []pluginapi.ResourceRoute   `json:"resources,omitempty"`
}

type managementRequest struct {
	pluginapi.ManagementRequest
}

// ---------------------------------------------------------------------------
// API response types
// ---------------------------------------------------------------------------

type summaryResponse struct {
	TotalRequests  int64   `json:"total_requests"`
	TotalTokens    int64   `json:"total_tokens"`
	InputTokens    int64   `json:"input_tokens"`
	OutputTokens   int64   `json:"output_tokens"`
	FailedRequests int64   `json:"failed_requests"`
	UniqueModels   int64   `json:"unique_models"`
	AvgLatencyMs   float64 `json:"avg_latency_ms"`
	CacheHitRate   float64 `json:"cache_hit_rate"`
	RangeHours     int     `json:"range_hours"`
}

type modelBreakdown struct {
	Model        string `json:"model"`
	Requests     int64  `json:"requests"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	TotalTokens  int64  `json:"total_tokens"`
}

type usageEvent struct {
	ID               int64  `json:"id"`
	Timestamp        string `json:"timestamp"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	TotalTokens      int64  `json:"total_tokens"`
	LatencyMs        int64  `json:"latency_ms"`
	Failed           bool    `json:"failed"`
	CacheHitRate     float64 `json:"cache_hit_rate"`
	FailureBody      string  `json:"failure_body,omitempty"`
	AuthID           string `json:"auth_id"`
	ExecutorType     string `json:"executor_type"`
}

type eventsResponse struct {
	Events []usageEvent `json:"events"`
	Total  int64        `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

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
		var decoded pluginConfig
		if errUnmarshal := yaml.Unmarshal(req.ConfigYAML, &decoded); errUnmarshal != nil {
			return errUnmarshal
		}
		cfg = mergeConfig(cfg, decoded)
	}
	activeConfig.Store(normalizeConfig(cfg))
	return nil
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
	return err
}

func loadRecentIntoRing() {
	cfg := currentConfig()
	ringBuf = make([]usageEvent, cfg.MaxInMemoryEvents)
	ringHead = 0
	ringCount = 0

	rows, err := db.Query(
		"SELECT id, timestamp, provider, model, input_tokens, output_tokens, total_tokens, latency_ms, failed, failure_body, auth_id, executor_type, cache_read_tokens FROM usage_events ORDER BY id DESC LIMIT ?",
		cfg.MaxInMemoryEvents,
	)
	if err != nil {
		return
	}
	defer rows.Close()

	var events []usageEvent
	for rows.Next() {
		var e usageEvent
		var crt int64
		if errScan := rows.Scan(&e.ID, &e.Timestamp, &e.Provider, &e.Model, &e.InputTokens, &e.OutputTokens, &e.TotalTokens, &e.LatencyMs, &e.Failed, &e.FailureBody, &e.AuthID, &e.ExecutorType, &crt); errScan != nil {
			continue
		}
		e.CacheHitRate = cacheHitRate(crt, e.InputTokens)
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
		CacheHitRate: cacheHitRate(record.Detail.CacheReadTokens, record.Detail.InputTokens),
		AuthID:       record.AuthID,
		ExecutorType: record.ExecutorType,
	}
	if record.Failed {
		event.FailureBody = record.Failure.Body
	}

	// Insert into SQLite
	result, errInsert := db.Exec(
		`INSERT INTO usage_events (timestamp, provider, model, alias, auth_id, auth_type, auth_index, api_key,
		 input_tokens, output_tokens, reasoning_tokens, total_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens,
		 latency_ms, ttft_ms, failed, failure_status_code, failure_body,
		 executor_type, source, service_tier)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.RequestedAt.Format(time.RFC3339),
		record.Provider,
		record.Model,
		record.Alias,
		record.AuthID,
		record.AuthType,
		record.AuthIndex,
		record.APIKey,
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
		return okEnvelope(handleSummary(req.Query))
	case strings.EqualFold(req.Method, http.MethodGet) && (path == resourceAPIModelsPath || path == managementModelsPath):
		return okEnvelope(handleModels(req.Query))
	case strings.EqualFold(req.Method, http.MethodGet) && (path == resourceAPIEventsPath || path == managementEventsPath):
		return okEnvelope(handleEvents(req.Query))
	case strings.EqualFold(req.Method, http.MethodPost) && path == managementCleanupPath:
		return okEnvelope(handleCleanup())
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

func handleSummary(query map[string][]string) pluginapi.ManagementResponse {
	rangeHours := parseRangeHours(query)

	// Detect Quotio caller: no range param means return the Quotio shape
	_, hasRange := query["range"]
	if !hasRange && len(query) == 0 {
		return handleQuotioUsage()
	}

	dbMu.RLock()
	d := db
	dbMu.RUnlock()

	var resp summaryResponse
	var cacheReadTotal int64
	resp.RangeHours = rangeHours

	since := time.Now().Add(-time.Duration(rangeHours) * time.Hour).Format(time.RFC3339)

	if d != nil {
		_ = d.QueryRow(
			"SELECT COUNT(*), COALESCE(SUM(total_tokens),0), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(failed),0), COUNT(DISTINCT model), COALESCE(AVG(latency_ms),0), COALESCE(SUM(cache_read_tokens),0) FROM usage_events WHERE timestamp >= ?",
			since,
		).Scan(&resp.TotalRequests, &resp.TotalTokens, &resp.InputTokens, &resp.OutputTokens, &resp.FailedRequests, &resp.UniqueModels, &resp.AvgLatencyMs, &cacheReadTotal)
		if resp.InputTokens > 0 {
			resp.CacheHitRate = float64(cacheReadTotal) / float64(resp.InputTokens) * 100
		}
	}

	return jsonResponse(http.StatusOK, resp)
}

// handleQuotioUsage returns the aggregate shape expected by Quotio (UsageStats).
// Maps to: GET /v0/management/usage and /v0/resource/plugins/usage-keeper/api/usage
type quotioUsageResponse struct {
	Usage         quotioUsageData `json:"usage"`
	FailedReqs    int64           `json:"failed_requests"`
}

type quotioUsageData struct {
	TotalRequests int64 `json:"total_requests"`
	SuccessCount  int64 `json:"success_count"`
	FailureCount  int64 `json:"failure_count"`
	TotalTokens   int64 `json:"total_tokens"`
	InputTokens   int64 `json:"input_tokens"`
	OutputTokens  int64 `json:"output_tokens"`
}

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
	dbMu.RLock()
	d := db
	dbMu.RUnlock()

	since := time.Now().Add(-time.Duration(rangeHours) * time.Hour).Format(time.RFC3339)
	models := make([]modelBreakdown, 0)

	if d != nil {
		rows, err := d.Query(
			"SELECT model, COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(total_tokens),0) FROM usage_events WHERE timestamp >= ? GROUP BY model ORDER BY SUM(total_tokens) DESC",
			since,
		)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var m modelBreakdown
				if errScan := rows.Scan(&m.Model, &m.Requests, &m.InputTokens, &m.OutputTokens, &m.TotalTokens); errScan == nil {
					models = append(models, m)
				}
			}
		}
	}

	return jsonResponse(http.StatusOK, models)
}

func handleEvents(query map[string][]string) pluginapi.ManagementResponse {
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

	since := time.Now().Add(-time.Duration(rangeHours) * time.Hour).Format(time.RFC3339)

	var resp eventsResponse
	resp.Limit = limit
	resp.Offset = offset

	dbMu.RLock()
	d := db
	dbMu.RUnlock()

	if d != nil {
		_ = d.QueryRow("SELECT COUNT(*) FROM usage_events WHERE timestamp >= ?", since).Scan(&resp.Total)

		rows, err := d.Query(
			"SELECT id, timestamp, provider, model, input_tokens, output_tokens, total_tokens, latency_ms, failed, failure_body, auth_id, executor_type, cache_read_tokens FROM usage_events WHERE timestamp >= ? ORDER BY id DESC LIMIT ? OFFSET ?",
			since, limit, offset,
		)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var e usageEvent
				var crtEvt int64
			if errScan := rows.Scan(&e.ID, &e.Timestamp, &e.Provider, &e.Model, &e.InputTokens, &e.OutputTokens, &e.TotalTokens, &e.LatencyMs, &e.Failed, &e.FailureBody, &e.AuthID, &e.ExecutorType, &crtEvt); errScan == nil {
				e.CacheHitRate = cacheHitRate(crtEvt, e.InputTokens)
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
// Embedded dashboard HTML
// ---------------------------------------------------------------------------

func renderDashboard() string {
	cfg := currentConfig()
	return fmt.Sprintf(dashboardHTML, cfg.RetentionDays, cfg.RefreshSeconds*1000, cfg.RefreshSeconds, cfg.RefreshSeconds)
}

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Usage Keeper – CPA Plugin</title>
<style>
/* Light theme (default, follows prefers-color-scheme) */
:root {
	--bg: #ffffff;
	--surface: #f6f8fa;
	--border: #d0d7de;
	--text: #1f2328;
	--text-secondary: #656d76;
	--accent: #0969da;
	--success: #1a7f37;
	--danger: #cf222e;
	--warning: #9a6700;
	--radius: 8px;
}
/* Dark theme */
[data-theme="dark"] {
	--bg: #0d1117;
	--surface: #161b22;
	--border: #30363d;
	--text: #c9d1d9;
	--text-secondary: #8b949e;
	--accent: #58a6ff;
	--success: #3fb950;
	--danger: #f85149;
	--warning: #d2991d;
}
@media (prefers-color-scheme: dark) {
	html:not([data-theme]) {
		--bg: #0d1117;
		--surface: #161b22;
		--border: #30363d;
		--text: #c9d1d9;
		--text-secondary: #8b949e;
		--accent: #58a6ff;
		--success: #3fb950;
		--danger: #f85149;
		--warning: #d2991d;
	}
}
* { margin: 0; padding: 0; box-sizing: border-box; }
body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif; background: var(--bg); color: var(--text); padding: 24px; line-height: 1.5; }
.header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 24px; flex-wrap: wrap; gap: 12px; }
.header h1 { font-size: 1.5rem; font-weight: 600; }
.header-right { display: flex; gap: 8px; align-items: center; }
.range-select { background: var(--surface); border: 1px solid var(--border); color: var(--text); padding: 6px 12px; border-radius: var(--radius); font-size: 0.85rem; cursor: pointer; }
.range-select:focus { outline: none; border-color: var(--accent); }
.auto-refresh { display: flex; align-items: center; gap: 6px; font-size: 0.8rem; color: var(--text-secondary); }
.auto-refresh .dot { width: 8px; height: 8px; border-radius: 50%%; background: var(--success); animation: pulse 2s infinite; }
.auto-refresh .dot.paused { background: var(--text-secondary); animation: none; }
.refresh-select { background: var(--surface); border: 1px solid var(--border); color: var(--text); padding: 4px 8px; border-radius: 4px; font-size: 0.75rem; cursor: pointer; }
.refresh-select:focus { outline: none; border-color: var(--accent); }
.theme-toggle { background: var(--surface); border: 1px solid var(--border); color: var(--text); cursor: pointer; font-size: 1rem; line-height: 1; border-radius: 4px; padding: 4px 8px; transition: background 0.15s; }
.theme-toggle:hover { background: var(--border); }
@keyframes pulse { 0%%, 100%% { opacity: 1; } 50%% { opacity: 0.4; } }
.cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); gap: 12px; margin-bottom: 24px; }
.card { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius); padding: 16px; }
.card-label { font-size: 0.75rem; color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.5px; margin-bottom: 4px; }
.card-value { font-size: 1.5rem; font-weight: 700; font-variant-numeric: tabular-nums; }
.card-sub { font-size: 0.8rem; color: var(--text-secondary); margin-top: 2px; }
.tabs { display: flex; gap: 0; margin-bottom: 16px; border-bottom: 1px solid var(--border); }
.tab { padding: 8px 16px; cursor: pointer; font-size: 0.9rem; color: var(--text-secondary); border-bottom: 2px solid transparent; transition: all 0.15s; background: none; border-top: none; border-left: none; border-right: none; }
.tab:hover { color: var(--text); }
.tab.active { color: var(--accent); border-bottom-color: var(--accent); }
.panel { display: none; }
.panel.active { display: block; }
table { width: 100%%; border-collapse: collapse; font-size: 0.85rem; }
th, td { padding: 8px 12px; text-align: left; border-bottom: 1px solid var(--border); }
th { color: var(--text-secondary); font-weight: 600; font-size: 0.8rem; text-transform: uppercase; }
tr:hover { background: rgba(88,166,255,0.04); }
.badge { display: inline-block; padding: 2px 6px; border-radius: 4px; font-size: 0.75rem; font-weight: 600; }
.badge-success { background: rgba(26,127,55,0.15); color: var(--success); }
.badge-danger { background: rgba(207,34,46,0.15); color: var(--danger); }
.model-bar { display: flex; align-items: center; gap: 8px; }
.model-bar-fill { height: 6px; background: var(--accent); border-radius: 3px; min-width: 2px; }
.model-bar-bg { flex: 1; height: 6px; background: var(--border); border-radius: 3px; overflow: hidden; }
.empty { text-align: center; padding: 40px; color: var(--text-secondary); }
@media (max-width: 600px) {
	body { padding: 12px; }
	.cards { grid-template-columns: repeat(2, 1fr); }
	.header { flex-direction: column; align-items: flex-start; }
}
</style>
</head>
<body>

<div class="header">
	<h1>Usage Keeper</h1>
	<div class="header-right">
		<select class="range-select" id="rangeSelect" onchange="refreshAll()">
			<option value="1h">Last hour</option>
			<option value="6h">Last 6 hours</option>
			<option value="24h" selected>Last 24 hours</option>
			<option value="7d">Last 7 days</option>
			<option value="30d">Last 30 days</option>
		</select>
		<div class="auto-refresh">
            <span class="dot" id="refreshDot"></span>
            <select class="refresh-select" id="refreshSelect" onchange="setRefreshInterval(this.value)">
                <option value="0" selected>Off</option>
                <option value="10">10s</option>
                <option value="30">30s</option>
                <option value="60">1m</option>
                <option value="300">5m</option>
                <option value="600">10m</option>
                <option value="1800">30m</option>
                <option value="3600">1h</option>
            </select>
            <button class="theme-toggle" id="refreshBtn" onclick="refreshAll()" title="Refresh now">&#8635;</button>
        </div>
		<button class="theme-toggle" id="themeToggle" onclick="toggleTheme()" title="Toggle light/dark theme">&#9788;</button>
	</div>
</div>

<div class="cards" id="cards"></div>

<div class="tabs">
	<button class="tab active" onclick="switchTab('models')">By Model</button>
	<button class="tab" onclick="switchTab('events')">All Events</button>
</div>

<div class="panel active" id="panel-models"></div>
<div class="panel" id="panel-events"></div>

<script>
(function() {
	const saved = localStorage.getItem('theme');
	if (saved) {
		document.documentElement.setAttribute('data-theme', saved);
	}
	updateThemeIcon();
})();

function toggleTheme() {
	const el = document.documentElement;
	const current = el.getAttribute('data-theme');
	const next = current === 'dark' ? 'light' : 'dark';
	el.setAttribute('data-theme', next);
	localStorage.setItem('theme', next);
	updateThemeIcon();
}

function updateThemeIcon() {
	const isDark = document.documentElement.getAttribute('data-theme') === 'dark';
	document.getElementById('themeToggle').innerHTML = isDark ? '&#9790;' : '&#9788;';
}

const API_BASE = window.location.pathname.replace(/\/dashboard$/, '') + '/api';

function switchTab(name) {
	document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
	document.querySelectorAll('.panel').forEach(p => p.classList.remove('active'));
	document.querySelector('[onclick="switchTab(\'' + name + '\')"]').classList.add('active');
	document.getElementById('panel-' + name).classList.add('active');
}

function formatNum(n) {
	if (n === undefined || n === null) return '0';
	if (n >= 1e9) return (n/1e9).toFixed(1) + 'B';
	if (n >= 1e6) return (n/1e6).toFixed(1) + 'M';
	if (n >= 1e3) return (n/1e3).toFixed(1) + 'K';
	return n.toLocaleString();
}

function formatMs(ms) {
	if (!ms || ms <= 0) return '\u2014';
	if (ms >= 1000) return (ms/1000).toFixed(1) + 's';
	return ms.toFixed(0) + 'ms';
}

function rangeValue() {
	return document.getElementById('rangeSelect').value;
}

async function fetchJSON(path) {
	const resp = await fetch(API_BASE + path + '?range=' + rangeValue());
	if (!resp.ok) throw new Error('HTTP ' + resp.status);
	return resp.json();
}

function renderCards(data) {
	document.getElementById('cards').innerHTML =
		'<div class="card"><div class="card-label">Requests</div><div class="card-value">' + formatNum(data.total_requests) + '</div><div class="card-sub">' + formatNum(data.failed_requests) + ' failed</div></div>' +
		'<div class="card"><div class="card-label">Tokens</div><div class="card-value">' + formatNum(data.total_tokens) + '</div><div class="card-sub">In: ' + formatNum(data.input_tokens) + ' / Out: ' + formatNum(data.output_tokens) + '</div></div>' +
		'<div class="card"><div class="card-label">Models</div><div class="card-value">' + formatNum(data.unique_models) + '</div><div class="card-sub">Avg latency ' + formatMs(data.avg_latency_ms) + '</div></div>' +
		'<div class="card"><div class="card-label">Retention</div><div class="card-value">%d</div><div class="card-sub">days</div></div>';
}

async function renderModels() {
	const models = await fetchJSON('/models');
	if (!models || models.length === 0) {
		document.getElementById('panel-models').innerHTML = '<div class="empty">No usage data yet for this time range.</div>';
		return;
	}
	const maxTokens = Math.max(...models.map(m => m.total_tokens), 1);
	let html = '<table><thead><tr><th>Model</th><th>Requests</th><th>Tokens</th><th>Input</th><th>Output</th></tr></thead><tbody>';
	for (const m of models) {
		const pct = Math.max((m.total_tokens / maxTokens) * 100, 1);
		html += '<tr><td>' + m.model + '</td><td>' + formatNum(m.requests) + '</td>' +
			'<td><div class="model-bar"><div class="model-bar-bg"><div class="model-bar-fill" style="width:' + pct + '%%"></div></div>' + formatNum(m.total_tokens) + '</div></td>' +
			'<td>' + formatNum(m.input_tokens) + '</td><td>' + formatNum(m.output_tokens) + '</td></tr>';
	}
	html += '</tbody></table>';
	document.getElementById('panel-models').innerHTML = html;
}

async function renderEvents() {
	const data = await fetchJSON('/events?limit=100');
	if (!data || !data.events || data.events.length === 0) {
		document.getElementById('panel-events').innerHTML = '<div class="empty">No events for this time range.</div>';
		return;
	}
	let html = '<table><thead><tr><th>Time</th><th>Model</th><th>Tokens</th><th>Cache</th><th>Latency</th><th>Status</th></tr></thead><tbody>';
	for (const e of data.events) {
		const timeStr = e.timestamp ? new Date(e.timestamp).toLocaleTimeString() : '\u2014';
		const statusBadge = e.failed
			? '<span class="badge badge-danger">FAIL</span>'
			: '<span class="badge badge-success">OK</span>';
		html += '<tr><td>' + timeStr + '</td><td title="' + e.provider + '">' + e.model + '</td>' +
			'<td>' + formatNum(e.total_tokens) + '</td><td>' + (e.cache_hit_rate || 0).toFixed(1) + '%%</td><td>' + formatMs(e.latency_ms) + '</td>' +
			'<td>' + statusBadge + '</td></tr>';
	}
	html += '</tbody></table>';
	if (data.total > 100) {
		html += '<p style="text-align:center;color:var(--text-secondary);margin-top:12px;font-size:0.8rem">Showing 100 of ' + formatNum(data.total) + ' events</p>';
	}
	document.getElementById('panel-events').innerHTML = html;
}

async function refreshAll() {
	const summary = await fetchJSON('/summary');
	renderCards(summary);
	await renderModels();
	if (document.getElementById('panel-events').classList.contains('active')) {
		await renderEvents();
	}
}

let refreshTimer = null;
let refreshIntervalMs = %d;

function setRefreshInterval(seconds) {
    refreshIntervalMs = parseInt(seconds) * 1000;
    if (refreshTimer) { clearInterval(refreshTimer); refreshTimer = null; }
    if (refreshIntervalMs > 0) {
        refreshTimer = setInterval(refreshAll, refreshIntervalMs);
        document.getElementById('refreshDot').classList.remove('paused');
    } else {
        document.getElementById('refreshDot').classList.add('paused');
    }
}

// Listen for tab switches to also refresh events on demand
const origSwitchTab = switchTab;
switchTab = function(name) {
	origSwitchTab(name);
	updateThemeIcon();
	if (name === 'events') renderEvents();
};

document.getElementById('refreshSelect').value = '%d';
setRefreshInterval(%d);

refreshAll();
</script>
</body>
</html>`

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
