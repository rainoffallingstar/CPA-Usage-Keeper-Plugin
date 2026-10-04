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
	activeConfig       atomic.Value
	db                 *sql.DB
	dbMu               sync.RWMutex
	initOnce           sync.Once
	shutdownOnce       sync.Once
	usageEventsWriteMu sync.Mutex
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
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) (code C.int) {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}

	// This plugin is loaded in-process by CPA, so a panic that crosses the cgo
	// boundary would terminate the whole host process. Never let one escape.
	defer func() {
		if r := recover(); r != nil {
			cacheMu.Lock()
			pluginPanics++
			cacheMu.Unlock()
			writeResponse(response, errorEnvelope("plugin_panic", fmt.Sprintf("recovered panic: %v", r)))
			code = 1
		}
	}()

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
		stopUsageWriter()
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
		initAllAccounts()
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
		if v, ok := intConfig(values, "refresh_seconds"); ok {
			decoded.RefreshSeconds = v
		}
		if v, ok := intConfig(values, "write_batch_size"); ok {
			decoded.WriteBatchSize = v
		}
		if v, ok := intConfig(values, "write_flush_seconds"); ok {
			decoded.WriteFlushSeconds = v
		}
		if v, ok := stringConfig(values, "api_key_hash_salt"); ok {
			decoded.APIKeyHashSalt = v
		}
		cfg = mergeConfig(cfg, decoded)
	}
	activeConfig.Store(normalizeConfig(cfg))
	return nil
}

var accountsInitOnce sync.Once

func initAllAccounts() {
	defer func() { recover() }()
	accountsInitOnce.Do(func() {
		cfg := currentConfig()
		initOpenCodeAccounts(cfg.OpenCodeGoAccounts)
		initGlmCodingAccounts(cfg.GlmCodingAccounts)
		initDeepseekAccounts(cfg.DeepSeekAccounts)
		initOllamaAccounts(cfg.OllamaAccounts)
		initColabAccounts()
		initModelPriceSync()
	})
}

// lazyInit defers all db.Query operations to the first API request, avoiding
// the modernc.org/sqlite + CGO crash that occurs during CPA startup.
var lazyInitOnce sync.Once

func lazyInit() {
	lazyInitOnce.Do(func() {
		defer func() { recover() }()
		loadOpenCodeAccountsFromDB()
		loadGlmAccountsFromDB()
		loadDeepseekAccountsFromDB()
		loadOllamaAccountsFromDB()
		loadColabAccountsFromDB()
		loadPricesFromDB()
	})
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
	if override.RefreshSeconds > 0 {
		base.RefreshSeconds = override.RefreshSeconds
	}
	if override.WriteBatchSize > 0 {
		base.WriteBatchSize = override.WriteBatchSize
	}
	if override.WriteFlushSeconds > 0 {
		base.WriteFlushSeconds = override.WriteFlushSeconds
	}
	if len(override.OpenCodeGoAccounts) > 0 {
		base.OpenCodeGoAccounts = override.OpenCodeGoAccounts
	}
	if len(override.GlmCodingAccounts) > 0 {
		base.GlmCodingAccounts = override.GlmCodingAccounts
	}
	if len(override.DeepSeekAccounts) > 0 {
		base.DeepSeekAccounts = override.DeepSeekAccounts
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
	if cfg.RefreshSeconds < 0 {
		cfg.RefreshSeconds = defaultRefreshSeconds
	} else if cfg.RefreshSeconds > 3600 {
		cfg.RefreshSeconds = defaultRefreshSeconds
	}
	if cfg.WriteBatchSize <= 0 {
		cfg.WriteBatchSize = defaultWriteBatchSize
	}
	if cfg.WriteBatchSize > 1000 {
		cfg.WriteBatchSize = 1000
	}
	if cfg.WriteFlushSeconds <= 0 {
		cfg.WriteFlushSeconds = defaultWriteFlushSeconds
	} else if cfg.WriteFlushSeconds > 300 {
		cfg.WriteFlushSeconds = defaultWriteFlushSeconds
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
					Name:        "refresh_seconds",
					Type:        pluginapi.ConfigFieldTypeInteger,
					Description: "Dashboard auto-refresh interval in seconds. 0 disables auto-refresh. Max 3600.",
				},
				{
					Name:        "write_batch_size",
					Type:        pluginapi.ConfigFieldTypeInteger,
					Description: "Usage events per SQLite transaction. Default 100; max 1000.",
				},
				{
					Name:        "write_flush_seconds",
					Type:        pluginapi.ConfigFieldTypeInteger,
					Description: "Maximum seconds an idle usage event waits before its batch is persisted. Default 10; max 300.",
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

func sqliteDatabaseDSN(databasePath string) string {
	return databasePath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_txlock=immediate"
}

// dbInitErr records a fatal database init failure instead of panicking, so the
// rest of the plugin can keep answering (and health can report it) rather than
// silently serving empty data.
var (
	dbInitErrMu sync.Mutex
	dbInitErr   string
)

func setDBInitError(msg string) {
	dbInitErrMu.Lock()
	dbInitErr = msg
	dbInitErrMu.Unlock()
}

func getDBInitError() string {
	dbInitErrMu.Lock()
	defer dbInitErrMu.Unlock()
	return dbInitErr
}

func ensureDB() {
	defer func() {
		if r := recover(); r != nil {
			setDBInitError(fmt.Sprintf("database init panicked: %v", r))
		}
	}()
	initOnce.Do(func() {
		cfg := currentConfig()
		var err error
		db, err = sql.Open("sqlite", sqliteDatabaseDSN(cfg.DBPath))
		if err != nil {
			setDBInitError("open database: " + err.Error())
			db = nil
			return
		}
		db.SetMaxOpenConns(3)
		db.SetMaxIdleConns(3)

		if errCreate := createTables(); errCreate != nil {
			setDBInitError("create tables: " + errCreate.Error())
			_ = db.Close()
			db = nil
			return
		}

		// Migration: add cache detail columns for existing databases
		_, _ = db.Exec("ALTER TABLE usage_events ADD COLUMN cache_read_tokens INTEGER NOT NULL DEFAULT 0")
		_, _ = db.Exec("ALTER TABLE usage_events ADD COLUMN cache_creation_tokens INTEGER NOT NULL DEFAULT 0")
	})
	startUsageWriter()
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
		CREATE INDEX IF NOT EXISTS idx_usage_events_ts_id ON usage_events(timestamp, id DESC);
		CREATE INDEX IF NOT EXISTS idx_usage_events_dt ON usage_events(datetime(timestamp));
	`)
	// Schema migration: add hashed_api_key for existing databases
	_, _ = db.Exec(`ALTER TABLE usage_events ADD COLUMN hashed_api_key TEXT NOT NULL DEFAULT ''`)
	// Create opencode quota accounts table for persistence
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS opencode_quota_accounts (
		name TEXT PRIMARY KEY,
		auth_cookie TEXT NOT NULL DEFAULT '',
		workspace_id TEXT NOT NULL DEFAULT ''
	)`)
	// Create GLM coding accounts table for persistence
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS glm_coding_accounts (
		name TEXT PRIMARY KEY,
		api_key TEXT NOT NULL DEFAULT '',
		base_url TEXT NOT NULL DEFAULT ''
	)`)
	// Create DeepSeek accounts table for persistence
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS deepseek_accounts (
		name TEXT PRIMARY KEY,
		api_key TEXT NOT NULL DEFAULT ''
	)`)
	// Create Ollama accounts table for persistence
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS ollama_accounts (
		name TEXT PRIMARY KEY,
		session_cookie TEXT NOT NULL DEFAULT '',
		show_session INTEGER NOT NULL DEFAULT 1,
		show_weekly INTEGER NOT NULL DEFAULT 1
	)`)
	// Create Google Colab accounts table for persistence
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS colab_quota_accounts (
		name TEXT PRIMARY KEY,
		refresh_token TEXT NOT NULL DEFAULT '',
		email TEXT NOT NULL DEFAULT ''
	)`)
	// Create Google Colab snapshot history table (points are recorded on each
	// quota refresh; older points are pruned by retention policy).
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS colab_usage_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		account TEXT NOT NULL,
		ts TEXT NOT NULL DEFAULT '',
		paid_balance REAL NOT NULL DEFAULT 0,
		free_remaining REAL NOT NULL DEFAULT 0,
		has_paid INTEGER NOT NULL DEFAULT 0,
		has_free INTEGER NOT NULL DEFAULT 0
	)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_colab_history_acct ON colab_usage_history(account, ts)`)
	// Create model prices table for persistence
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS model_prices (
		model TEXT PRIMARY KEY,
		prompt REAL NOT NULL DEFAULT 0,
		completion REAL NOT NULL DEFAULT 0,
		cache REAL NOT NULL DEFAULT 0,
		auto_synced INTEGER NOT NULL DEFAULT 0,
		updated_at TEXT NOT NULL DEFAULT ''
	)`)
	return err
}

// ---------------------------------------------------------------------------
// Usage event handling
// ---------------------------------------------------------------------------

// usageWriterState holds the bounded async persistence queue. This channel is
// the real in-memory buffer: when it fills up, events are dropped (and counted
// in storageQueueDrops), unlike the removed ring buffer which never dropped.
var usageWriterState struct {
	sync.Mutex
	queue   chan pluginapi.UsageRecord
	stop    chan struct{}
	done    chan struct{}
	started bool
}

func startUsageWriter() {
	usageWriterState.Lock()
	defer usageWriterState.Unlock()
	if usageWriterState.started {
		return
	}

	queueCapacity := currentConfig().WriteBatchSize * 10
	if queueCapacity < 1000 {
		queueCapacity = 1000
	}
	usageWriterState.queue = make(chan pluginapi.UsageRecord, queueCapacity)
	usageWriterState.stop = make(chan struct{})
	usageWriterState.done = make(chan struct{})
	usageWriterState.started = true
	go runUsageWriter(usageWriterState.queue, usageWriterState.stop, usageWriterState.done)
}

func stopUsageWriter() {
	usageWriterState.Lock()
	if !usageWriterState.started {
		usageWriterState.Unlock()
		return
	}
	stop := usageWriterState.stop
	done := usageWriterState.done
	usageWriterState.started = false
	close(stop)
	usageWriterState.Unlock()

	<-done

	usageWriterState.Lock()
	usageWriterState.queue = nil
	usageWriterState.stop = nil
	usageWriterState.done = nil
	usageWriterState.Unlock()
}

func enqueueUsageEvent(record pluginapi.UsageRecord) {
	usageWriterState.Lock()
	defer usageWriterState.Unlock()
	if !usageWriterState.started || usageWriterState.queue == nil {
		return
	}
	select {
	case usageWriterState.queue <- record:
	default:
		cacheMu.Lock()
		storageQueueDrops++
		cacheMu.Unlock()
	}
}

// usageWriteQueueStats returns the current depth and capacity of the async
// persistence queue. This is the real buffer pressure signal for the dashboard.
func usageWriteQueueStats() (used, capacity int) {
	usageWriterState.Lock()
	defer usageWriterState.Unlock()
	if usageWriterState.queue == nil {
		return 0, 0
	}
	return len(usageWriterState.queue), cap(usageWriterState.queue)
}

func runUsageWriter(queue <-chan pluginapi.UsageRecord, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)

	config := currentConfig()
	batchSize := config.WriteBatchSize
	flushInterval := time.Duration(config.WriteFlushSeconds) * time.Second
	timer := time.NewTimer(flushInterval)
	defer timer.Stop()

	batch := make([]pluginapi.UsageRecord, 0, batchSize)
	persistedEventsSinceCleanup := 0
	flush := func() {
		if len(batch) == 0 {
			return
		}
		batchLength := len(batch)
		if err := persistUsageBatchWithRetry(batch); err != nil {
			cacheMu.Lock()
			storageErrCount++
			cacheMu.Unlock()
		} else {
			persistedEventsSinceCleanup += batchLength
			if persistedEventsSinceCleanup >= 1000 {
				persistedEventsSinceCleanup = 0
				cleanupOldRecords(currentConfig().RetentionDays)
			}
		}
		batch = batch[:0]
	}

	for {
		select {
		case event := <-queue:
			batch = append(batch, event)
			if len(batch) >= batchSize {
				flush()
				resetUsageWriterTimer(timer, flushInterval)
			}
		case <-timer.C:
			flush()
			resetUsageWriterTimer(timer, flushInterval)
		case <-stop:
			for {
				select {
				case event := <-queue:
					batch = append(batch, event)
				default:
					flush()
					return
				}
			}
		}
	}
}

func resetUsageWriterTimer(timer *time.Timer, interval time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(interval)
}

func persistUsageBatchWithRetry(batch []pluginapi.UsageRecord) error {
	const maximumAttempts = 3
	const initialRetryDelay = 100 * time.Millisecond

	var persistError error
	for attempt := 0; attempt < maximumAttempts; attempt++ {
		persistError = persistUsageBatch(batch)
		if persistError == nil {
			return nil
		}
		if !isSQLiteBusyError(persistError) || attempt == maximumAttempts-1 {
			return persistError
		}

		time.Sleep(initialRetryDelay << attempt)
	}
	return persistError
}

func isSQLiteBusyError(err error) bool {
	errorMessage := strings.ToLower(err.Error())
	return strings.Contains(errorMessage, "database is locked") || strings.Contains(errorMessage, "database is busy")
}

func persistUsageBatch(batch []pluginapi.UsageRecord) error {
	usageEventsWriteMu.Lock()
	defer usageEventsWriteMu.Unlock()

	startedAt := time.Now()
	dbMu.RLock()
	database := db
	dbMu.RUnlock()
	if database == nil {
		return fmt.Errorf("usage database is not available")
	}

	transaction, errBegin := database.Begin()
	if errBegin != nil {
		return errBegin
	}
	defer transaction.Rollback()

	statement, errPrepare := transaction.Prepare(`INSERT INTO usage_events (timestamp, provider, model, alias, auth_id, auth_type, auth_index, api_key, hashed_api_key,
		input_tokens, output_tokens, reasoning_tokens, total_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens,
		latency_ms, ttft_ms, failed, failure_status_code, failure_body,
		executor_type, source, service_tier)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if errPrepare != nil {
		return errPrepare
	}
	defer statement.Close()

	for index := range batch {
		record := batch[index]
		_, errExecute := statement.Exec(
			record.RequestedAt.Format(time.RFC3339),
			record.Provider,
			record.Model,
			record.Alias,
			record.AuthID,
			record.AuthType,
			record.AuthIndex,
			maskedAPIKey(record.APIKey),
			hashedAPIKey(record.APIKey),
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
		if errExecute != nil {
			return errExecute
		}
	}

	if errCommit := transaction.Commit(); errCommit != nil {
		return errCommit
	}

	cacheMu.Lock()
	lastWriteMs = time.Since(startedAt).Milliseconds()
	dashboardVersion++
	cacheMu.Unlock()
	return nil
}

func maskedAPIKey(apiKey string) string {
	if looksLikeSecretKey(apiKey) {
		return maskAPIKey(apiKey)
	}
	return apiKey
}

func hashedAPIKey(apiKey string) string {
	if apiKey == "" {
		return ""
	}
	return hashAPIKey(apiKey)
}

func handleUsage(raw []byte) ([]byte, error) {
	ensureDB()
	lazyInit()

	var record pluginapi.UsageRecord
	if errUnmarshal := json.Unmarshal(raw, &record); errUnmarshal != nil {
		return okEnvelopeJSON("{}")
	}

	enqueueUsageEvent(record)
	return okEnvelopeJSON("{}")
}

func cleanupOldRecords(retentionDays int) {
	usageEventsWriteMu.Lock()
	defer usageEventsWriteMu.Unlock()

	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays).Format(time.RFC3339)
	_, _ = d.Exec("DELETE FROM usage_events WHERE datetime(timestamp) < datetime(?)", cutoff)
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
			{Method: http.MethodPost, Path: "/usage-keeper/prices"},
			{Method: http.MethodGet, Path: "/usage-keeper/export"},
			{Method: http.MethodPost, Path: "/usage-keeper/export-jobs"},
			{Method: http.MethodGet, Path: "/usage-keeper/export-jobs"},
			{Method: http.MethodGet, Path: "/usage-keeper/export-download"},
			{Method: http.MethodDelete, Path: "/usage-keeper/export-jobs"},
			{Method: http.MethodPost, Path: "/usage-keeper/import"},
			{Method: http.MethodGet, Path: "/usage-keeper/opencode-quota"},
			{Method: http.MethodPost, Path: "/usage-keeper/opencode-quota"},
			{Method: http.MethodGet, Path: "/usage-keeper/glmcoding-quota"},
			{Method: http.MethodPost, Path: "/usage-keeper/glmcoding-quota"},
			{Method: http.MethodGet, Path: "/usage-keeper/ollama-quota"},
			{Method: http.MethodPost, Path: "/usage-keeper/ollama-quota"},
			{Method: http.MethodGet, Path: "/usage-keeper/colab-quota"},
			{Method: http.MethodPost, Path: "/usage-keeper/colab-quota"},
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
				Path:        "/api/timeseries",
				Menu:        "",
				Description: "Bucketed usage time series JSON API.",
			},
			{
				Path:        "/api/usage",
				Menu:        "",
				Description: "Quotio-compatible aggregate usage JSON API.",
			},
			{
				Path:        "/api/health",
				Menu:        "",
				Description: "Health monitoring JSON API.",
			},
			{
				Path:        "/api/prices",
				Menu:        "",
				Description: "Model pricing JSON API.",
			},
			{
				Path:        "/api/prices/sync",
				Menu:        "",
				Description: "Trigger model price sync from modelprice.boxtech.icu.",
			},
			{
				Path:        "/api/opencode-quota",
				Menu:        "",
				Description: "OpenCode Go quota JSON API.",
			},
			{
				Path:        "/api/glmcoding-quota",
				Menu:        "",
				Description: "GLM Coding Plan quota JSON API.",
			},
			{
				Path:        "/api/deepseek-quota",
				Menu:        "",
				Description: "DeepSeek balance JSON API.",
			},
			{
				Path:        "/api/ollama-quota",
				Menu:        "",
				Description: "Ollama Cloud quota JSON API.",
			},
			{
				Path:        "/api/colab-quota",
				Menu:        "",
				Description: "Google Colab subscription & quota JSON API.",
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Management request dispatch
// ---------------------------------------------------------------------------

func handleManagement(raw []byte) ([]byte, error) {
	ensureDB()
	lazyInit()

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
	case strings.EqualFold(req.Method, http.MethodGet) && (path == resourceAPISummaryPath || path == managementSummaryPath):
		return okEnvelope(handleSummary(req.Query, req.Headers))
	case strings.EqualFold(req.Method, http.MethodGet) && (path == managementUsageCompatPath || path == resourceAPIUsagePath):
		return okEnvelope(handleQuotioUsage())
	case strings.EqualFold(req.Method, http.MethodGet) && (path == resourceAPIModelsPath || path == managementModelsPath):
		return okEnvelope(handleModels(req.Query))
	case strings.EqualFold(req.Method, http.MethodGet) && (path == resourceAPIEventsPath || path == managementEventsPath):
		return okEnvelope(handleEvents(req.Query, req.Headers))
	case strings.EqualFold(req.Method, http.MethodGet) && path == resourceAPITimeseriesPath:
		return okEnvelope(handleTimeseries(req.Query, req.Headers))
	case strings.EqualFold(req.Method, http.MethodPost) && path == managementCleanupPath:
		return okEnvelope(handleCleanup())
	case strings.EqualFold(req.Method, http.MethodGet) && strings.HasSuffix(path, "/health"):
		return okEnvelope(handleHealthCheck())
	case strings.EqualFold(req.Method, http.MethodGet) && strings.HasSuffix(path, "/prices/sync"):
		return okEnvelope(handlePriceSyncImpl())
	case strings.EqualFold(req.Method, http.MethodGet) && strings.HasSuffix(path, "/prices"):
		return okEnvelope(handleGetPricesWithActions(req.Query))
	case strings.EqualFold(req.Method, http.MethodPut) && strings.HasSuffix(path, "/prices"):
		return okEnvelope(handlePutPrice(req.Body))
	case strings.EqualFold(req.Method, http.MethodDelete) && strings.HasSuffix(path, "/prices"):
		return okEnvelope(handleDeletePrice(req.Query))
	case strings.EqualFold(req.Method, http.MethodPost) && strings.HasSuffix(path, "/prices"):
		return okEnvelope(handlePricesPost(req.Body))
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
	case strings.EqualFold(req.Method, http.MethodGet) && strings.HasSuffix(path, "/opencode-quota"):
		return okEnvelope(handleOpenCodeQuotaGet(req.Query))
	case strings.EqualFold(req.Method, http.MethodPost) && strings.HasSuffix(path, "/opencode-quota"):
		return okEnvelope(handleOpenCodeQuotaPost(req.Body))
	case strings.EqualFold(req.Method, http.MethodGet) && strings.HasSuffix(path, "/glmcoding-quota"):
		return okEnvelope(handleGlmCodingQuotaGet(req.Query))
	case strings.EqualFold(req.Method, http.MethodGet) && strings.HasSuffix(path, "/deepseek-quota"):
		return okEnvelope(handleDeepseekQuotaGet(req.Query))
	case strings.EqualFold(req.Method, http.MethodGet) && strings.HasSuffix(path, "/ollama-quota"):
		return okEnvelope(handleOllamaQuotaGet(req.Query))
	case strings.EqualFold(req.Method, http.MethodPost) && strings.HasSuffix(path, "/ollama-quota"):
		return okEnvelope(handleOllamaQuotaPost(req.Body))
	case strings.EqualFold(req.Method, http.MethodGet) && strings.HasSuffix(path, "/colab-quota"):
		return okEnvelope(handleColabQuotaGet(req.Query))
	case strings.EqualFold(req.Method, http.MethodPost) && strings.HasSuffix(path, "/colab-quota"):
		return okEnvelope(handleColabQuotaPost(req.Body))
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
		return 30 * 24
	}
}

func handleSummary(query map[string][]string, headers map[string][]string) pluginapi.ManagementResponse {
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
			"SELECT COUNT(*), COALESCE(SUM(total_tokens),0), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(failed),0), COUNT(DISTINCT model), COALESCE(AVG(latency_ms),0), COALESCE(SUM(cached_tokens),0) FROM usage_events WHERE datetime(timestamp) >= datetime(?)",
			since,
		).Scan(&resp.TotalRequests, &resp.TotalTokens, &resp.InputTokens, &resp.OutputTokens, &resp.FailedRequests, &resp.UniqueModels, &resp.AvgLatencyMs, &cacheReadTotal)
		resp.CacheHitRate = cacheHitRate(cacheReadTotal, resp.InputTokens)
	}

	return jsonResponse(http.StatusOK, resp)
}

// handleTimeseries aggregates usage into fixed time buckets across the whole
// requested range. The dashboard previously bucketed only the most recent page
// of events (limit=500), so a 30-day chart really showed the last few hours.
// This aggregates every event in range and prices each bucket with the real
// per-model prices, so the chart matches the summary KPIs.
func handleTimeseries(query map[string][]string, headers map[string][]string) pluginapi.ManagementResponse {
	rangeHours := parseRangeHours(query)
	bucketCount := 12
	if vals, ok := query["buckets"]; ok && len(vals) > 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(vals[0])); err == nil && n >= 2 && n <= 60 {
			bucketCount = n
		}
	}

	// ETag includes the data version, so a 304 is returned without re-running
	// the per-bucket aggregation the overview triggers on every refresh.
	etag := dashboardWeakETag("timeseries", fmt.Sprintf("%d-%d", rangeHours, bucketCount))
	if checkETag(headers, etag) {
		return notModifiedResponse(etag)
	}

	resp := timeseriesResponse{RangeHours: rangeHours, Buckets: bucketCount, Series: make([]timeseriesBucket, 0, bucketCount)}

	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil {
		return jsonResponse(http.StatusOK, resp)
	}

	now := time.Now()
	start := now.Add(-time.Duration(rangeHours) * time.Hour)
	step := time.Duration(rangeHours) * time.Hour / time.Duration(bucketCount)
	if step <= 0 {
		step = time.Hour
	}

	for i := 0; i < bucketCount; i++ {
		bStart := start.Add(time.Duration(i) * step)
		bEnd := bStart.Add(step)
		if i == bucketCount-1 {
			bEnd = now
		}
		bucket := timeseriesBucket{Start: bStart.Format(time.RFC3339), End: bEnd.Format(time.RFC3339)}
		if rangeHours > 48 {
			bucket.Label = fmt.Sprintf("%d/%d", bEnd.Month(), bEnd.Day())
		} else {
			bucket.Label = bEnd.Format("15:04")
		}

		// The last bucket is inclusive of "now" so the newest events are counted.
		where := "datetime(timestamp) >= datetime(?) AND datetime(timestamp) < datetime(?)"
		if i == bucketCount-1 {
			where = "datetime(timestamp) >= datetime(?) AND datetime(timestamp) <= datetime(?)"
		}
		// Aggregate per model so each bucket can be priced with computeCost.
		rows, errQuery := d.Query(
			"SELECT model, COUNT(*), COALESCE(SUM(total_tokens),0), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(cached_tokens),0) FROM usage_events WHERE "+where+" GROUP BY model",
			bStart.Format(time.RFC3339), bEnd.Format(time.RFC3339),
		)
		if errQuery == nil {
			for rows.Next() {
				var model string
				var requests, tokens, input, output, cached int64
				if errScan := rows.Scan(&model, &requests, &tokens, &input, &output, &cached); errScan == nil {
					bucket.Requests += requests
					bucket.Tokens += tokens
					bucket.InputTokens += input
					bucket.OutputTokens += output
					bucket.CachedTokens += cached
					bucket.Cost += computeCost(model, input, output, cached)
				}
			}
			rows.Close()
		}
		resp.Series = append(resp.Series, bucket)
	}

	return jsonResponseWithETag(http.StatusOK, resp, etag)
}

// handleQuotioUsage returns the aggregate shape expected by Quotio (UsageStats).

func handleQuotioUsage() pluginapi.ManagementResponse {
	dbMu.RLock()
	d := db
	dbMu.RUnlock()

	if d == nil {
		return jsonResponse(http.StatusOK, quotioUsageResponse{})
	}

	// Use 30-day window by default
	since := time.Now().Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	var total, failed, totalToks, inputToks, outputToks int64
	_ = d.QueryRow(
		"SELECT COUNT(*), COALESCE(SUM(failed),0), COALESCE(SUM(total_tokens),0), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0) FROM usage_events WHERE datetime(timestamp) >= datetime(?)",
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
				"SELECT provider, model, COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(total_tokens),0), COALESCE(SUM(cached_tokens),0) FROM usage_events WHERE datetime(timestamp) >= datetime(?) AND provider = ? GROUP BY provider, model ORDER BY SUM(total_tokens) DESC",
				since, provider,
			)
		} else {
			rows, err = d.Query(
				"SELECT COALESCE(NULLIF(GROUP_CONCAT(DISTINCT provider),\"\"),\"multiple\") as provider, model, COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(total_tokens),0), COALESCE(SUM(cached_tokens),0) FROM usage_events WHERE datetime(timestamp) >= datetime(?) GROUP BY model ORDER BY SUM(total_tokens) DESC",
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
	rangeHours := 30 * 24

	if vals, ok := query["limit"]; ok && len(vals) > 0 {
		if n, errParse := parseInt(vals[0]); errParse == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	if vals, ok := query["offset"]; ok && len(vals) > 0 {
		if n, errParse := parseInt(vals[0]); errParse == nil && n >= 0 && n <= 1000000 {
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
	executorFilter := ""
	if vals, ok := query["executor"]; ok && len(vals) > 0 {
		executorFilter = strings.TrimSpace(vals[0])
	}
	// failed: "" = all, "1"/"true" = only failures, "0"/"false" = only successes
	failedFilter := ""
	if vals, ok := query["failed"]; ok && len(vals) > 0 {
		switch strings.ToLower(strings.TrimSpace(vals[0])) {
		case "1", "true", "yes":
			failedFilter = "1"
		case "0", "false", "no":
			failedFilter = "0"
		}
	}
	// q is a free-text search applied server-side across model/auth/executor.
	qFilter := ""
	if vals, ok := query["q"]; ok && len(vals) > 0 {
		qFilter = strings.TrimSpace(vals[0])
		if len(qFilter) > 100 {
			qFilter = qFilter[:100]
		}
	}

	filterKey := fmt.Sprintf("%s-%s-%s-%s-%s-%s", modelFilter, sourceFilter, authFilter, executorFilter, failedFilter, qFilter)

	// ETag for response caching (checked inside the response cache layer)
	etag := dashboardWeakETag("events", fmt.Sprintf("%d-%d-%d-%s", limit, offset, rangeHours, filterKey))

	since := time.Now().Add(-time.Duration(rangeHours) * time.Hour).Format(time.RFC3339)

	var resp eventsResponse
	resp.Limit = limit
	resp.Offset = offset

	// Check in-memory response cache (keyed by all query parameters)
	cacheKey := fmt.Sprintf("ev-%d-%d-%d-%s", limit, offset, rangeHours, filterKey)
	responseCacheMu.RLock()
	if cached, ok := eventsResponseCache[cacheKey]; ok && time.Since(cached.cachedAt) < responseCacheTTL {
		// Check ETag for 304 response
		if checkETag(headers, cached.etag) {
			responseCacheMu.RUnlock()
			cacheMu.Lock()
			eventsCacheHits++
			cacheMu.Unlock()
			return notModifiedResponse(cached.etag)
		}
		responseCacheMu.RUnlock()
		cacheMu.Lock()
		eventsCacheHits++
		cacheMu.Unlock()
		return jsonResponseWithETag(http.StatusOK, cached.response, cached.etag)
	}
	responseCacheMu.RUnlock()

	dbMu.RLock()
	d := db
	dbMu.RUnlock()

	if d != nil {
		// Build dynamic query with filters.
		// datetime() normalises any RFC3339 offset to UTC, so range comparisons
		// and ordering stay correct even if stored rows mix offsets (e.g. after
		// a machine timezone/DST change) - plain string comparison would not.
		where := "WHERE datetime(timestamp) >= datetime(?)"
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
		if executorFilter != "" {
			where += " AND executor_type = ?"
			args = append(args, executorFilter)
		}
		if failedFilter != "" {
			where += " AND failed = ?"
			failedInt := 0
			if failedFilter == "1" {
				failedInt = 1
			}
			args = append(args, failedInt)
		}
		if qFilter != "" {
			like := "%" + qFilter + "%"
			where += " AND (model LIKE ? OR auth_id LIKE ? OR executor_type LIKE ?)"
			args = append(args, like, like, like)
		}

		// Capped count: only scan up to 10001 rows to determine if total exceeds 10000.
		// The frontend only shows "Showing 100 of N", so an exact count for large N is unnecessary.
		countArgs := append([]interface{}{}, args...)
		_ = d.QueryRow("SELECT COUNT(*) FROM (SELECT 1 FROM usage_events "+where+" LIMIT 10001)", countArgs...).Scan(&resp.Total)

		queryArgs := append(args, limit, offset)
		// COALESCE keeps rows with NULL columns from being silently dropped when
		// scanned into plain Go strings/ints.
		rows, err := d.Query(
			"SELECT id, COALESCE(timestamp,''), COALESCE(provider,''), COALESCE(model,''), COALESCE(input_tokens,0), COALESCE(output_tokens,0), COALESCE(reasoning_tokens,0), COALESCE(total_tokens,0), COALESCE(cached_tokens,0), COALESCE(cache_read_tokens,0), COALESCE(cache_creation_tokens,0), COALESCE(latency_ms,0), COALESCE(ttft_ms,0), COALESCE(failed,0), COALESCE(failure_status_code,0), COALESCE(failure_body,''), COALESCE(auth_id,''), COALESCE(executor_type,''), COALESCE(source,''), COALESCE(service_tier,'') FROM usage_events "+where+" ORDER BY datetime(timestamp) DESC, id DESC LIMIT ? OFFSET ?",
			queryArgs...,
		)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var e usageEvent
				var cachedEvt int64
				if errScan := rows.Scan(&e.ID, &e.Timestamp, &e.Provider, &e.Model, &e.InputTokens, &e.OutputTokens, &e.ReasoningTokens, &e.TotalTokens, &e.CachedTokens, &e.CacheReadTokens, &e.CacheCreationTokens, &e.LatencyMs, &e.TTFTMs, &e.Failed, &e.FailureStatusCode, &e.FailureBody, &e.AuthID, &e.ExecutorType, &e.Source, &e.ServiceTier); errScan == nil {
					cachedEvt = e.CachedTokens
					e.CacheHitRate = cacheHitRate(cachedEvt, e.InputTokens)
					resp.Events = append(resp.Events, e)
				}
			}
		}
	}

	// Store in response cache
	responseCacheMu.Lock()
	eventsResponseCache[cacheKey] = eventsCacheEntry{response: resp, etag: etag, cachedAt: time.Now()}
	responseCacheMu.Unlock()

	cacheMu.Lock()
	eventsCacheMisses++
	cacheMu.Unlock()

	return jsonResponseWithETag(http.StatusOK, resp, etag)
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
	// Two %%d placeholders in template: one for RetentionDays card value,
	// one for refreshIntervalMs initial value in ms.
	return fmt.Sprintf(dashboardHTML, cfg.RetentionDays, cfg.RefreshSeconds*1000)
}

// ---------------------------------------------------------------------------
// Helper functions
// ---------------------------------------------------------------------------
// cacheHitRate returns the cache hit rate as a percentage, or 0 if input tokens is zero.
//
// The rate is clamped to [0, 100]. Providers that report cached tokens separately
// from input tokens (e.g. Claude's cache_read_input_tokens) can yield
// cached > input, which would otherwise produce a nonsensical value above 100%.
func cacheHitRate(cacheRead, inputTokens int64) float64 {
	if inputTokens <= 0 {
		return 0
	}
	rate := float64(cacheRead) / float64(inputTokens) * 100
	if rate < 0 {
		return 0
	}
	if rate > 100 {
		return 100
	}
	return rate
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

func jsonResponseWithETag(statusCode int, body any, etag string) pluginapi.ManagementResponse {
	resp := jsonResponse(statusCode, body)
	resp.Headers["ETag"] = []string{etag}
	resp.Headers["Cache-Control"] = []string{"private, no-cache"}
	return resp
}

func htmlResponse(statusCode int, body string) pluginapi.ManagementResponse {
	return pluginapi.ManagementResponse{
		StatusCode: statusCode,
		Headers: http.Header{
			"Content-Type": {contentTypeHTML},
			// The dashboard is hot-reloaded (the dylib is swapped in place), so
			// the browser must revalidate instead of serving a stale cached page
			// after an update.
			"Cache-Control": {"no-store, must-revalidate"},
			"Pragma":        {"no-cache"},
		},
		Body: []byte(body),
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
