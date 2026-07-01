package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// Config tests
// ---------------------------------------------------------------------------

func TestDefaultConfig(t *testing.T) {
	cfg := defaultConfig()
	if cfg.DBPath != defaultDBPath {
		t.Fatalf("default DBPath = %q, want %q", cfg.DBPath, defaultDBPath)
	}
	if cfg.RetentionDays != defaultRetentionDays {
		t.Fatalf("default RetentionDays = %d, want %d", cfg.RetentionDays, defaultRetentionDays)
	}
	if cfg.MaxInMemoryEvents != defaultMaxInMemoryEvents {
		t.Fatalf("default MaxInMemoryEvents = %d, want %d", cfg.MaxInMemoryEvents, defaultMaxInMemoryEvents)
	}
	if cfg.RefreshSeconds != defaultRefreshSeconds {
		t.Fatalf("default RefreshSeconds = %d, want %d", cfg.RefreshSeconds, defaultRefreshSeconds)
	}
}

func TestNormalizeConfig(t *testing.T) {
	tests := []struct {
		name string
		in   pluginConfig
		want pluginConfig
	}{
		{
			name: "empty config gets defaults",
			in:   pluginConfig{},
			want: pluginConfig{
				DBPath:            defaultDBPath,
				RetentionDays:     defaultRetentionDays,
				MaxInMemoryEvents: defaultMaxInMemoryEvents,
				RefreshSeconds:    defaultRefreshSeconds,
			},
		},
		{
			name: "explicit db_path",
			in:   pluginConfig{DBPath: "/custom/path.db"},
			want: pluginConfig{
				DBPath:            "/custom/path.db",
				RetentionDays:     defaultRetentionDays,
				MaxInMemoryEvents: defaultMaxInMemoryEvents,
				RefreshSeconds:    defaultRefreshSeconds,
			},
		},
		{
			name: "max_in_memory_events capped",
			in:   pluginConfig{MaxInMemoryEvents: 20000},
			want: pluginConfig{
				DBPath:            defaultDBPath,
				RetentionDays:     defaultRetentionDays,
				MaxInMemoryEvents: 10000,
				RefreshSeconds:    defaultRefreshSeconds,
			},
		},
		{
			name: "refresh_seconds out of range",
			in:   pluginConfig{RefreshSeconds: 5000},
			want: pluginConfig{
				DBPath:            defaultDBPath,
				RetentionDays:     defaultRetentionDays,
				MaxInMemoryEvents: defaultMaxInMemoryEvents,
				RefreshSeconds:    defaultRefreshSeconds,
			},
		},
		{
			name: "negative refresh_seconds reset",
			in:   pluginConfig{RefreshSeconds: -1},
			want: pluginConfig{
				DBPath:            defaultDBPath,
				RetentionDays:     defaultRetentionDays,
				MaxInMemoryEvents: defaultMaxInMemoryEvents,
				RefreshSeconds:    defaultRefreshSeconds,
			},
		},
		{
			name: "valid custom config",
			in: pluginConfig{
				DBPath:            "/data/db.sqlite",
				RetentionDays:     7,
				MaxInMemoryEvents: 500,
				RefreshSeconds:    30,
			},
			want: pluginConfig{
				DBPath:            "/data/db.sqlite",
				RetentionDays:     7,
				MaxInMemoryEvents: 500,
				RefreshSeconds:    30,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeConfig(tt.in)
			if got.DBPath != tt.want.DBPath {
				t.Errorf("DBPath = %q, want %q", got.DBPath, tt.want.DBPath)
			}
			if got.RetentionDays != tt.want.RetentionDays {
				t.Errorf("RetentionDays = %d, want %d", got.RetentionDays, tt.want.RetentionDays)
			}
			if got.MaxInMemoryEvents != tt.want.MaxInMemoryEvents {
				t.Errorf("MaxInMemoryEvents = %d, want %d", got.MaxInMemoryEvents, tt.want.MaxInMemoryEvents)
			}
			if got.RefreshSeconds != tt.want.RefreshSeconds {
				t.Errorf("RefreshSeconds = %d, want %d", got.RefreshSeconds, tt.want.RefreshSeconds)
			}
		})
	}
}

func TestMergeConfig(t *testing.T) {
	base := defaultConfig()
	override := pluginConfig{
		DBPath:            "/override.db",
		RetentionDays:     14,
		MaxInMemoryEvents: 2000,
		RefreshSeconds:    60,
	}
	result := mergeConfig(base, override)
	if result.DBPath != "/override.db" {
		t.Errorf("DBPath = %q, want /override.db", result.DBPath)
	}
	if result.RetentionDays != 14 {
		t.Errorf("RetentionDays = %d, want 14", result.RetentionDays)
	}
	if result.MaxInMemoryEvents != 2000 {
		t.Errorf("MaxInMemoryEvents = %d, want 2000", result.MaxInMemoryEvents)
	}
	if result.RefreshSeconds != 60 {
		t.Errorf("RefreshSeconds = %d, want 60", result.RefreshSeconds)
	}
}

func TestMergeConfigEmptyOverridePreservesDefaults(t *testing.T) {
	base := defaultConfig()
	result := mergeConfig(base, pluginConfig{})
	if result.DBPath != defaultDBPath {
		t.Errorf("DBPath = %q, want %q", result.DBPath, defaultDBPath)
	}
	if result.RetentionDays != defaultRetentionDays {
		t.Errorf("RetentionDays = %d, want %d", result.RetentionDays, defaultRetentionDays)
	}
}

func TestCurrentConfig(t *testing.T) {
	// Reset to default
	activeConfig.Store(defaultConfig())

	cfg := currentConfig()
	if cfg.DBPath != defaultDBPath {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, defaultDBPath)
	}

	// Set a new config
	activeConfig.Store(pluginConfig{DBPath: "/test.db", RetentionDays: 30, MaxInMemoryEvents: 500, RefreshSeconds: 0})

	cfg = currentConfig()
	if cfg.DBPath != "/test.db" {
		t.Errorf("DBPath = %q, want /test.db", cfg.DBPath)
	}
	if cfg.RetentionDays != 30 {
		t.Errorf("RetentionDays = %d, want 30", cfg.RetentionDays)
	}

	// Reset for other tests
	activeConfig.Store(defaultConfig())
}

// ---------------------------------------------------------------------------
// Helper function tests
// ---------------------------------------------------------------------------

func TestCacheHitRate(t *testing.T) {
	tests := []struct {
		cached, input int64
		want          float64
	}{
		{50, 100, 50},
		{0, 100, 0},
		{0, 0, 0},
		{100, 50, 200}, // over 100% capped at 200 -> capped later in dashboard
	}

	for _, tt := range tests {
		got := cacheHitRate(tt.cached, tt.input)
		if got != tt.want {
			t.Errorf("cacheHitRate(%d, %d) = %f, want %f", tt.cached, tt.input, got, tt.want)
		}
	}
}

func TestBoolToInt(t *testing.T) {
	if boolToInt(true) != 1 {
		t.Errorf("boolToInt(true) = %d, want 1", boolToInt(true))
	}
	if boolToInt(false) != 0 {
		t.Errorf("boolToInt(false) = %d, want 0", boolToInt(false))
	}
}

func TestParseInt(t *testing.T) {
	tests := []struct {
		in    string
		want  int
		isErr bool
	}{
		{"100", 100, false},
		{"0", 0, false},
		{"-1", -1, false},
		{"abc", 0, true},
		{"", 0, true},
	}

	for _, tt := range tests {
		got, err := parseInt(tt.in)
		if (err != nil) != tt.isErr {
			t.Errorf("parseInt(%q) error = %v, want error = %v", tt.in, err, tt.isErr)
		}
		if !tt.isErr && got != tt.want {
			t.Errorf("parseInt(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestParseRangeHours(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"1h", 1},
		{"6h", 6},
		{"24h", 24},
		{"day", 24},
		{"7d", 168},
		{"week", 168},
		{"30d", 720},
		{"month", 720},
		{"", 24},
		{"invalid", 24},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			query := map[string][]string{}
			if tt.in != "" {
				query["range"] = []string{tt.in}
			}
			got := parseRangeHours(query)
			if got != tt.want {
				t.Errorf("parseRangeHours(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// JSON response tests
// ---------------------------------------------------------------------------

func TestJSONResponse(t *testing.T) {
	data := map[string]any{"key": "value"}
	resp := jsonResponse(200, data)
	if resp.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
	}
	if v, ok := resp.Headers["Content-Type"]; !ok || v[0] != contentTypeJSON {
		t.Errorf("Content-Type header = %v, want %s", resp.Headers["Content-Type"], contentTypeJSON)
	}
	var decoded map[string]any
	if err := json.Unmarshal(resp.Body, &decoded); err != nil {
		t.Fatalf("failed to unmarshal body: %v", err)
	}
	if decoded["key"] != "value" {
		t.Errorf("body key = %v, want value", decoded["key"])
	}
}

func TestHTMLResponse(t *testing.T) {
	resp := htmlResponse(200, "test host")
	if resp.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
	}
	if v, ok := resp.Headers["Content-Type"]; !ok || v[0] != contentTypeHTML {
		t.Errorf("Content-Type header = %v, want %s", resp.Headers["Content-Type"], contentTypeHTML)
	}
	if string(resp.Body) != "test host" {
		t.Errorf("body = %q, want test host", string(resp.Body))
	}
}

func TestEnvelopeHelpers(t *testing.T) {
	err := errorEnvelope("test_error", "test message")
	var env envelope
	if errUnmarshal := json.Unmarshal(err, &env); errUnmarshal != nil {
		t.Fatalf("failed to unmarshal: %v", errUnmarshal)
	}
	if env.OK {
		t.Fatal("error envelope should be ok=false")
	}
	if env.Error.Code != "test_error" {
		t.Errorf("error code = %q, want test_error", env.Error.Code)
	}
}

func TestOkEnvelope(t *testing.T) {
	in := map[string]any{"result": "ok"}
	raw, err := okEnvelope(in)
	if err != nil {
		t.Fatalf("okEnvelope() error = %v", err)
	}
	var env envelope
	if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal != nil {
		t.Fatalf("failed to unmarshal: %v", errUnmarshal)
	}
	if !env.OK {
		t.Fatal("ok envelope should be ok=true")
	}
}

func TestOkEnvelopeJSON(t *testing.T) {
	raw, err := okEnvelopeJSON(`{"test":true}`)
	if err != nil {
		t.Fatalf("okEnvelopeJSON() error = %v", err)
	}
	var env envelope
	if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal != nil {
		t.Fatalf("failed to unmarshal: %v", errUnmarshal)
	}
	if !env.OK {
		t.Fatal("ok envelope should be ok=true")
	}
	var result map[string]bool
	if errUnmarshal := json.Unmarshal(env.Result, &result); errUnmarshal != nil {
		t.Fatalf("failed to unmarshal result: %v", errUnmarshal)
	}
	if !result["test"] {
		t.Fatal("result.test should be true")
	}
}

// ---------------------------------------------------------------------------
// Plugin registration tests
// ---------------------------------------------------------------------------

func TestPluginRegistration(t *testing.T) {
	reg := pluginRegistration()
	if reg.SchemaVersion == 0 {
		t.Fatal("schema version should not be 0")
	}
	if reg.Metadata.Name != "Usage Keeper" {
		t.Errorf("name = %q, want Usage Keeper", reg.Metadata.Name)
	}
	if !reg.Capabilities.UsagePlugin {
		t.Fatal("usage_plugin capability should be true")
	}
	if !reg.Capabilities.ManagementAPI {
		t.Fatal("management_api capability should be true")
	}
	if len(reg.Metadata.ConfigFields) != 4 {
		t.Errorf("expected 4 config fields, got %d", len(reg.Metadata.ConfigFields))
	}
}

func TestManagementRegResponse(t *testing.T) {
	resp := managementRegResponse()
	if len(resp.Routes) < 4 {
		t.Errorf("expected at least 4 routes, got %d", len(resp.Routes))
	}
	if len(resp.Resources) < 4 {
		t.Errorf("expected at least 4 resources, got %d", len(resp.Resources))
	}

	// Check Quotio compat route
	found := false
	for _, r := range resp.Routes {
		if r.Path == "/usage" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected /usage route for Quotio compatibility")
	}
}

// ---------------------------------------------------------------------------
// Database and summary tests
// ---------------------------------------------------------------------------

// setupTestDB creates a temporary SQLite database for testing.
func setupTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()

	// Save original global state
	origDB := db

	tmpFile, err := os.CreateTemp("", "usage-keeper-test-*.db")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmpFile.Close()

	d, err := sql.Open("sqlite", tmpFile.Name())
	if err != nil {
		os.Remove(tmpFile.Name())
		t.Fatalf("failed to open test db: %v", err)
	}

	// Init schema
	_, err = d.Exec(`CREATE TABLE IF NOT EXISTS usage_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp TEXT,
		provider TEXT,
		model TEXT,
		alias TEXT,
		auth_id TEXT,
		auth_type TEXT,
		auth_index TEXT,
		api_key TEXT,
		hashed_api_key TEXT,
		input_tokens INTEGER,
		output_tokens INTEGER,
		reasoning_tokens INTEGER,
		total_tokens INTEGER,
		cached_tokens INTEGER,
		cache_read_tokens INTEGER,
		cache_creation_tokens INTEGER,
		latency_ms INTEGER,
		ttft_ms INTEGER,
		failed INTEGER DEFAULT 0,
		failure_status_code INTEGER,
		failure_body TEXT,
		executor_type TEXT,
		source TEXT,
		service_tier TEXT
	)`)
	if err != nil {
		d.Close()
		os.Remove(tmpFile.Name())
		t.Fatalf("failed to create schema: %v", err)
	}
	// Migration: add hashed_api_key column for existing test DBs
	_, _ = d.Exec(`ALTER TABLE usage_events ADD COLUMN hashed_api_key TEXT NOT NULL DEFAULT ''`)

	dbMu.Lock()
	db = d
	dbMu.Unlock()

	// Init ring buffer
	ringMu.Lock()
	ringBuf = make([]usageEvent, 1000)
	ringHead = 0
	ringCount = 0
	ringMu.Unlock()

	cleanup := func() {
		dbMu.Lock()
		if db != nil {
			db.Close()
		}
		db = origDB
		dbMu.Unlock()
		os.Remove(tmpFile.Name())

		// Restore ring buffer
		ringMu.Lock()
		ringBuf = nil
		ringHead = 0
		ringCount = 0
		ringMu.Unlock()
	}

	return d, cleanup
}

func insertTestEvent(t *testing.T, d *sql.DB, provider, model string, input, output, total int64, failed bool, timestamp string) {
	t.Helper()
	failedInt := 0
	if failed {
		failedInt = 1
	}
	_, err := d.Exec(
		`INSERT INTO usage_events (timestamp, provider, model, alias, auth_id, auth_type, auth_index, api_key,
		 input_tokens, output_tokens, reasoning_tokens, total_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens,
		 latency_ms, ttft_ms, failed, failure_status_code, failure_body,
		 executor_type, source, service_tier)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		timestamp, provider, model, "", "", "", "", "",
		input, output, 0, total, 0, 0, 0,
		100, 50, failedInt, 0, "",
		"test", "", "default",
	)
	if err != nil {
		t.Fatalf("failed to insert test event: %v", err)
	}
}

func TestHandleSummaryWithDB(t *testing.T) {
	d, cleanup := setupTestDB(t)
	defer cleanup()
	_ = d

	now := time.Now().Format(time.RFC3339)
	hourAgo := time.Now().Add(-30 * time.Minute).Format(time.RFC3339)

	insertTestEvent(t, d, "openai", "gpt-4", 100, 50, 150, false, now)
	insertTestEvent(t, d, "openai", "gpt-4", 200, 100, 300, false, hourAgo)
	insertTestEvent(t, d, "deepseek", "deepseek-v3", 500, 200, 700, true, hourAgo)

	query := map[string][]string{"range": {"1h"}}
	resp := handleSummary(query, nil)

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var summary summaryResponse
	if err := json.Unmarshal(resp.Body, &summary); err != nil {
		t.Fatalf("failed to unmarshal summary: %v", err)
	}

	if summary.TotalRequests != 3 {
		t.Errorf("total_requests = %d, want 3", summary.TotalRequests)
	}
	if summary.TotalTokens != 1150 {
		t.Errorf("total_tokens = %d, want 1150", summary.TotalTokens)
	}
	if summary.FailedRequests != 1 {
		t.Errorf("failed_requests = %d, want 1", summary.FailedRequests)
	}
	if summary.UniqueModels != 2 {
		t.Errorf("unique_models = %d, want 2", summary.UniqueModels)
	}
	if summary.RangeHours != 1 {
		t.Errorf("range_hours = %d, want 1", summary.RangeHours)
	}
}

func TestHandleQuotioUsage(t *testing.T) {
	d, cleanup := setupTestDB(t)
	defer cleanup()
	_ = d

	now := time.Now().Format(time.RFC3339)
	insertTestEvent(t, d, "openai", "gpt-4", 100, 50, 150, false, now)
	insertTestEvent(t, d, "openai", "gpt-4", 200, 100, 300, false, now)
	insertTestEvent(t, d, "deepseek", "deepseek-v3", 500, 200, 700, true, now)

	resp := handleQuotioUsage()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var qr quotioUsageResponse
	if err := json.Unmarshal(resp.Body, &qr); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if qr.Usage.TotalRequests != 3 {
		t.Errorf("total_requests = %d, want 3", qr.Usage.TotalRequests)
	}
	if qr.Usage.SuccessCount != 2 {
		t.Errorf("success_count = %d, want 2", qr.Usage.SuccessCount)
	}
	if qr.Usage.FailureCount != 1 {
		t.Errorf("failure_count = %d, want 1", qr.Usage.FailureCount)
	}
	if qr.Usage.TotalTokens != 1150 {
		t.Errorf("total_tokens = %d, want 1150", qr.Usage.TotalTokens)
	}
}

func TestHandleModels(t *testing.T) {
	d, cleanup := setupTestDB(t)
	defer cleanup()
	_ = d

	now := time.Now().Format(time.RFC3339)
	insertTestEvent(t, d, "openai", "gpt-4", 100, 50, 150, false, now)
	insertTestEvent(t, d, "openai", "gpt-4", 200, 100, 300, false, now)
	insertTestEvent(t, d, "deepseek", "deepseek-v3", 500, 200, 700, false, now)

	resp := handleModels(map[string][]string{"range": {"1h"}})
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var models []modelBreakdown
	if err := json.Unmarshal(resp.Body, &models); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(models) != 2 {
		t.Fatalf("expected 2 model groups, got %d", len(models))
	}

	// Models sorted by total_tokens DESC
	if models[0].Model != "deepseek-v3" {
		t.Errorf("first model = %q, want deepseek-v3", models[0].Model)
	}
	if models[0].TotalTokens != 700 {
		t.Errorf("first model total = %d, want 700", models[0].TotalTokens)
	}
	if models[1].Model != "gpt-4" {
		t.Errorf("second model = %q, want gpt-4", models[1].Model)
	}
}

func TestHandleModelsWithProviderFilter(t *testing.T) {
	d, cleanup := setupTestDB(t)
	defer cleanup()
	_ = d

	now := time.Now().Format(time.RFC3339)
	insertTestEvent(t, d, "openai", "gpt-4", 100, 50, 150, false, now)
	insertTestEvent(t, d, "openai", "gpt-3.5", 50, 25, 75, false, now)
	insertTestEvent(t, d, "deepseek", "deepseek-v3", 500, 200, 700, false, now)

	resp := handleModels(map[string][]string{"range": {"1h"}, "provider": {"openai"}})
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var models []modelBreakdown
	if err := json.Unmarshal(resp.Body, &models); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(models) != 2 {
		t.Fatalf("expected 2 models for openai, got %d", len(models))
	}
	for _, m := range models {
		if m.Provider != "openai" {
			t.Errorf("provider = %q, want openai", m.Provider)
		}
	}
}

func TestHandleEvents(t *testing.T) {
	d, cleanup := setupTestDB(t)
	defer cleanup()
	_ = d

	now := time.Now().Format(time.RFC3339)
	insertTestEvent(t, d, "openai", "gpt-4", 100, 50, 150, false, now)
	insertTestEvent(t, d, "openai", "gpt-3.5", 50, 25, 75, false, now)
	insertTestEvent(t, d, "deepseek", "deepseek-v3", 500, 200, 700, true, now)

	resp := handleEvents(map[string][]string{"limit": {"10"}, "range": {"1h"}}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var events eventsResponse
	if err := json.Unmarshal(resp.Body, &events); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if events.Total != 3 {
		t.Errorf("total = %d, want 3", events.Total)
	}
	if len(events.Events) != 3 {
		t.Errorf("events count = %d, want 3", len(events.Events))
	}
	if events.Limit != 10 {
		t.Errorf("limit = %d, want 10", events.Limit)
	}
}

func TestHandleEventsPagination(t *testing.T) {
	d, cleanup := setupTestDB(t)
	defer cleanup()
	_ = d

	now := time.Now().Format(time.RFC3339)
	for i := 0; i < 5; i++ {
		insertTestEvent(t, d, "openai", "gpt-4", 100, 50, 150, false, now)
	}

	resp := handleEvents(map[string][]string{"limit": {"2"}, "offset": {"1"}, "range": {"1h"}}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var events eventsResponse
	if err := json.Unmarshal(resp.Body, &events); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if events.Total != 5 {
		t.Errorf("total = %d, want 5", events.Total)
	}
	if len(events.Events) != 2 {
		t.Errorf("events count = %d, want 2", len(events.Events))
	}
	if events.Offset != 1 {
		t.Errorf("offset = %d, want 1", events.Offset)
	}
}

func TestHandleEventsLimitCapped(t *testing.T) {
	d, cleanup := setupTestDB(t)
	defer cleanup()
	_ = d

	now := time.Now().Format(time.RFC3339)
	for i := 0; i < 3; i++ {
		insertTestEvent(t, d, "openai", "gpt-4", 100, 50, 150, false, now)
	}

	// Request limit > 500 should be capped
	resp := handleEvents(map[string][]string{"limit": {"1000"}, "range": {"1h"}}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var events eventsResponse
	if err := json.Unmarshal(resp.Body, &events); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if events.Limit != 50 {
		t.Errorf("limit should default to 50 for invalid large values, got %d", events.Limit)
	}
}

func TestHandleCleanup(t *testing.T) {
	d, cleanup := setupTestDB(t)
	defer cleanup()
	_ = d

	// Set config with short retention
	cfg := defaultConfig()
	cfg.RetentionDays = 1
	activeConfig.Store(cfg)
	defer activeConfig.Store(defaultConfig())

	// Insert events: one old, one recent
	oldTime := time.Now().Add(-48 * time.Hour).Format(time.RFC3339)
	recentTime := time.Now().Format(time.RFC3339)

	insertTestEvent(t, d, "openai", "gpt-4", 100, 50, 150, false, oldTime)
	insertTestEvent(t, d, "openai", "gpt-4", 200, 100, 300, false, recentTime)

	resp := handleCleanup()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Count remaining events
	var count int
	err := d.QueryRow("SELECT COUNT(*) FROM usage_events").Scan(&count)
	if err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 1 {
		t.Errorf("remaining events = %d, want 1", count)
	}
}

// ---------------------------------------------------------------------------
// Dashboard rendering test
// ---------------------------------------------------------------------------

func TestRenderDashboardContainsRequiredElements(t *testing.T) {
	html := renderDashboard()
	if len(html) < 100 {
		t.Fatalf("dashboard HTML too short: %d bytes", len(html))
	}

	required := []string{
		"Usage Keeper",
		"dashboard",
		"summary",
		"models",
		"events",
	}
	for _, s := range required {
		if !containsStr(html, s) {
			t.Errorf("dashboard HTML missing %q", s)
		}
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstr(s, substr)
}

func searchSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// configure test
// ---------------------------------------------------------------------------

func TestConfigure(t *testing.T) {
	// Reset config
	activeConfig.Store(defaultConfig())
	defer activeConfig.Store(defaultConfig())

	// Test with empty request
	err := configure(nil)
	if err != nil {
		t.Fatalf("configure(nil) error = %v", err)
	}

	// Test with invalid JSON
	err = configure([]byte("not json"))
	if err == nil {
		t.Fatal("configure with invalid JSON should error")
	}
}

func TestConfigureWithYAML(t *testing.T) {
	activeConfig.Store(defaultConfig())
	defer activeConfig.Store(defaultConfig())

	yaml := `db_path: /custom/path.db
retention_days: 14
max_in_memory_events: 500
refresh_seconds: 30
`
	req, _ := json.Marshal(map[string]any{
		"config_yaml": []byte(yaml),
	})

	err := configure(req)
	if err != nil {
		t.Fatalf("configure() error = %v", err)
	}

	cfg := currentConfig()
	if cfg.DBPath != "/custom/path.db" {
		t.Errorf("DBPath = %q, want /custom/path.db", cfg.DBPath)
	}
	if cfg.RetentionDays != 14 {
		t.Errorf("RetentionDays = %d, want 14", cfg.RetentionDays)
	}
	if cfg.MaxInMemoryEvents != 500 {
		t.Errorf("MaxInMemoryEvents = %d, want 500", cfg.MaxInMemoryEvents)
	}
	if cfg.RefreshSeconds != 30 {
		t.Errorf("RefreshSeconds = %d, want 30", cfg.RefreshSeconds)
	}
}

// ---------------------------------------------------------------------------
// method dispatch tests
// ---------------------------------------------------------------------------

func TestHandleMethodUnknown(t *testing.T) {
	result, err := handleMethod("unknown.method", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var env envelope
	if errUnmarshal := json.Unmarshal(result, &env); errUnmarshal != nil {
		t.Fatalf("failed to unmarshal: %v", errUnmarshal)
	}
	if env.OK {
		t.Fatal("unknown method should return error envelope")
	}
	if env.Error.Code != "unknown_method" {
		t.Errorf("error code = %q, want unknown_method", env.Error.Code)
	}
}

func TestHandleMethodEmptyMethod(t *testing.T) {
	result, err := handleMethod("", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var env envelope
	if errUnmarshal := json.Unmarshal(result, &env); errUnmarshal != nil {
		t.Fatalf("failed to unmarshal: %v", errUnmarshal)
	}
	if env.OK {
		t.Fatal("empty method should return error envelope (ok=false)")
	}
}
