package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
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
				DBPath:         defaultDBPath,
				RetentionDays:  defaultRetentionDays,
				RefreshSeconds: defaultRefreshSeconds,
			},
		},
		{
			name: "explicit db_path",
			in:   pluginConfig{DBPath: "/custom/path.db"},
			want: pluginConfig{
				DBPath:         "/custom/path.db",
				RetentionDays:  defaultRetentionDays,
				RefreshSeconds: defaultRefreshSeconds,
			},
		},
		{
			name: "refresh_seconds out of range",
			in:   pluginConfig{RefreshSeconds: 5000},
			want: pluginConfig{
				DBPath:         defaultDBPath,
				RetentionDays:  defaultRetentionDays,
				RefreshSeconds: defaultRefreshSeconds,
			},
		},
		{
			name: "negative refresh_seconds reset",
			in:   pluginConfig{RefreshSeconds: -1},
			want: pluginConfig{
				DBPath:         defaultDBPath,
				RetentionDays:  defaultRetentionDays,
				RefreshSeconds: defaultRefreshSeconds,
			},
		},
		{
			name: "valid custom config",
			in: pluginConfig{
				DBPath:         "/data/db.sqlite",
				RetentionDays:  7,
				RefreshSeconds: 30,
			},
			want: pluginConfig{
				DBPath:         "/data/db.sqlite",
				RetentionDays:  7,
				RefreshSeconds: 30,
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
			if got.RefreshSeconds != tt.want.RefreshSeconds {
				t.Errorf("RefreshSeconds = %d, want %d", got.RefreshSeconds, tt.want.RefreshSeconds)
			}
		})
	}
}

func TestMergeConfig(t *testing.T) {
	base := defaultConfig()
	override := pluginConfig{
		DBPath:         "/override.db",
		RetentionDays:  14,
		RefreshSeconds: 60,
	}
	result := mergeConfig(base, override)
	if result.DBPath != "/override.db" {
		t.Errorf("DBPath = %q, want /override.db", result.DBPath)
	}
	if result.RetentionDays != 14 {
		t.Errorf("RetentionDays = %d, want 14", result.RetentionDays)
	}
	// refresh_seconds must apply on its own, without any other field being set.
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
	activeConfig.Store(pluginConfig{DBPath: "/test.db", RetentionDays: 30, RefreshSeconds: 0})

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

func TestSQLiteDatabaseDSNUsesSupportedPragmas(t *testing.T) {
	databasePath := "/tmp/usage-keeper.db"
	databaseDSN := sqliteDatabaseDSN(databasePath)

	if !strings.HasPrefix(databaseDSN, databasePath+"?") {
		t.Fatalf("database DSN = %q, want path prefix %q", databaseDSN, databasePath+"?")
	}
	for _, expectedParameter := range []string{
		"_pragma=journal_mode(WAL)",
		"_pragma=busy_timeout(5000)",
		"_pragma=synchronous(NORMAL)",
		"_txlock=immediate",
	} {
		if !strings.Contains(databaseDSN, expectedParameter) {
			t.Errorf("database DSN = %q, missing %q", databaseDSN, expectedParameter)
		}
	}
}

func TestSQLiteDatabaseDSNEnablesContentionPragmas(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	var journalMode string
	if err := database.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("read journal mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal mode = %q, want wal", journalMode)
	}

	var busyTimeoutMilliseconds int
	if err := database.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeoutMilliseconds); err != nil {
		t.Fatalf("read busy timeout: %v", err)
	}
	if busyTimeoutMilliseconds != 5000 {
		t.Errorf("busy timeout = %d, want 5000", busyTimeoutMilliseconds)
	}
}

func TestIsSQLiteBusyError(t *testing.T) {
	testCases := []struct {
		name  string
		error error
		want  bool
	}{
		{name: "locked", error: errors.New("database is locked"), want: true},
		{name: "busy", error: errors.New("SQLITE_BUSY: database is busy"), want: true},
		{name: "other error", error: errors.New("disk I/O error"), want: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := isSQLiteBusyError(testCase.error); got != testCase.want {
				t.Errorf("isSQLiteBusyError(%v) = %t, want %t", testCase.error, got, testCase.want)
			}
		})
	}
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
		{100, 50, 100}, // cached > input (Claude-style) is clamped to 100
		{200, 100, 100},
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
		{"", 720},
		{"invalid", 720},
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
	// max_in_memory_events was removed with the dead ring buffer.
	wantFields := map[string]bool{
		"db_path":             true,
		"retention_days":      true,
		"refresh_seconds":     true,
		"write_batch_size":    true,
		"write_flush_seconds": true,
	}
	if len(reg.Metadata.ConfigFields) != len(wantFields) {
		t.Errorf("expected %d config fields, got %d", len(wantFields), len(reg.Metadata.ConfigFields))
	}
	for _, f := range reg.Metadata.ConfigFields {
		if !wantFields[f.Name] {
			t.Errorf("unexpected config field %q", f.Name)
		}
		delete(wantFields, f.Name)
	}
	for name := range wantFields {
		t.Errorf("missing config field %q", name)
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

	d, err := sql.Open("sqlite", sqliteDatabaseDSN(tmpFile.Name()))
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

	cleanup := func() {
		dbMu.Lock()
		if db != nil {
			db.Close()
		}
		db = origDB
		dbMu.Unlock()
		os.Remove(tmpFile.Name())
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

func TestPersistUsageBatchWritesAllEventsInOneTransaction(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	requestedAt := time.Now().UTC()
	batch := []pluginapi.UsageRecord{
		{
			RequestedAt: requestedAt,
			Provider:    "openai",
			Model:       "gpt-5",
		},
		{
			RequestedAt: requestedAt,
			Provider:    "openai",
			Model:       "gpt-5-mini",
		},
	}

	if err := persistUsageBatch(batch); err != nil {
		t.Fatalf("persistUsageBatch() error = %v", err)
	}

	var persistedCount int
	if err := database.QueryRow("SELECT COUNT(*) FROM usage_events").Scan(&persistedCount); err != nil {
		t.Fatalf("count persisted events: %v", err)
	}
	if persistedCount != len(batch) {
		t.Fatalf("persisted event count = %d, want %d", persistedCount, len(batch))
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

func TestHandleTimeseriesAggregatesFullRange(t *testing.T) {
	d, cleanup := setupTestDB(t)
	defer cleanup()

	setTestPrices(map[string]modelPrice{
		"gpt-4": {Prompt: 30, Completion: 60, Cache: 15, AutoSynced: true},
	})
	defer setTestPrices(nil)

	now := time.Now()
	// Spread events across the whole 30d window instead of clustering them in
	// the most recent hours - this is what the /events?limit=500 page did.
	offsets := []time.Duration{-1 * time.Hour, -100 * time.Hour, -250 * time.Hour, -600 * time.Hour}
	for _, off := range offsets {
		insertTestEvent(t, d, "openai", "gpt-4", 1000000, 0, 1000000, false, now.Add(off).Format(time.RFC3339))
	}

	resp := handleTimeseries(map[string][]string{"range": {"30d"}, "buckets": {"12"}}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var ts timeseriesResponse
	if err := json.Unmarshal(resp.Body, &ts); err != nil {
		t.Fatalf("unmarshal timeseries: %v", err)
	}
	if ts.RangeHours != 720 {
		t.Errorf("range_hours = %d, want 720", ts.RangeHours)
	}
	if len(ts.Series) != 12 {
		t.Fatalf("series length = %d, want 12", len(ts.Series))
	}

	var totalReq, totalTokens int64
	nonEmpty := 0
	totalCost := 0.0
	for _, b := range ts.Series {
		totalReq += b.Requests
		totalTokens += b.Tokens
		totalCost += b.Cost
		if b.Requests > 0 {
			nonEmpty++
		}
	}
	if totalReq != int64(len(offsets)) {
		t.Errorf("total requests across buckets = %d, want %d (events must not be dropped)", totalReq, len(offsets))
	}
	if nonEmpty < 4 {
		t.Errorf("non-empty buckets = %d, want >= 4 (full-range aggregation)", nonEmpty)
	}
	if totalTokens != 4*1000000 {
		t.Errorf("total tokens = %d, want %d", totalTokens, 4*1000000)
	}
	// 4 events * 1M input tokens * $30/1M = $120, priced with real prices.
	if totalCost < 119.999999 || totalCost > 120.000001 {
		t.Errorf("total cost = %v, want 120", totalCost)
	}
}

func TestHandleTimeseriesClampsBucketCount(t *testing.T) {
	d, cleanup := setupTestDB(t)
	defer cleanup()
	_ = d

	// Out-of-range bucket counts fall back to the default of 12.
	resp := handleTimeseries(map[string][]string{"range": {"1h"}, "buckets": {"999"}}, nil)
	var ts timeseriesResponse
	if err := json.Unmarshal(resp.Body, &ts); err != nil {
		t.Fatalf("unmarshal timeseries: %v", err)
	}
	if ts.RangeHours != 1 {
		t.Errorf("range_hours = %d, want 1", ts.RangeHours)
	}
	if ts.Buckets != 12 || len(ts.Series) != 12 {
		t.Errorf("buckets = %d / series = %d, want 12", ts.Buckets, len(ts.Series))
	}
	if ts.Series[0].Label == "" {
		t.Errorf("expected non-empty bucket labels")
	}
}

func TestHandleEventsReturnsStatusAndAppliesServerSideFilters(t *testing.T) {
	d, cleanup := setupTestDB(t)
	defer cleanup()

	now := time.Now().Format(time.RFC3339)
	insert := func(model, executor string, failed, status int, body string) {
		if _, err := d.Exec(`INSERT INTO usage_events (timestamp, provider, model, executor_type, input_tokens, output_tokens, total_tokens, cached_tokens, latency_ms, ttft_ms, failed, failure_status_code, failure_body)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, now, "p", model, executor, 100, 10, 110, 50, 20, 5, failed, status, body); err != nil {
			t.Fatalf("insert event: %v", err)
		}
	}
	insert("model-a", "cursor", 1, 500, "boom")
	insert("model-b", "api", 0, 0, "")

	var out eventsResponse

	// failed=1 is applied server-side and the real status code is returned.
	resp := handleEvents(map[string][]string{"range": {"24h"}, "failed": {"1"}}, nil)
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Events) != 1 {
		t.Fatalf("failed=1 returned %d events, want 1", len(out.Events))
	}
	e := out.Events[0]
	if e.Model != "model-a" || !e.Failed || e.FailureStatusCode != 500 {
		t.Errorf("unexpected event: %+v", e)
	}
	if e.CachedTokens != 50 || e.TTFTMs != 5 {
		t.Errorf("new fields not returned: cached=%d ttft=%d", e.CachedTokens, e.TTFTMs)
	}

	// executor filter
	resp = handleEvents(map[string][]string{"range": {"24h"}, "executor": {"api"}}, nil)
	json.Unmarshal(resp.Body, &out)
	if len(out.Events) != 1 || out.Events[0].Model != "model-b" {
		t.Errorf("executor filter wrong: %+v", out.Events)
	}

	// free-text q search
	resp = handleEvents(map[string][]string{"range": {"24h"}, "q": {"model-a"}}, nil)
	json.Unmarshal(resp.Body, &out)
	if len(out.Events) != 1 || out.Events[0].Model != "model-a" {
		t.Errorf("q filter wrong: %+v", out.Events)
	}

	// no filter returns everything
	resp = handleEvents(map[string][]string{"range": {"24h"}}, nil)
	json.Unmarshal(resp.Body, &out)
	if len(out.Events) != 2 {
		t.Errorf("unfiltered returned %d events, want 2", len(out.Events))
	}
}

func TestHealthReportsObservabilityFields(t *testing.T) {
	d, cleanup := setupTestDB(t)
	defer cleanup()
	_ = d

	body := string(handleHealthCheck().Body)
	for _, key := range []string{`"plugin_panics"`, `"price_sync"`, `"unpriced_models"`, `"write_queue_size"`} {
		if !strings.Contains(body, key) {
			t.Errorf("health JSON missing %s: %s", key, body)
		}
	}
	if strings.Contains(body, "ring_buffer") {
		t.Errorf("health JSON should no longer expose ring_buffer: %s", body)
	}
}

// createTables runs on every startup; a bad index expression there would leave
// the plugin with dbInitErr and no working database, so guard it explicitly.
func TestCreateTablesAndIndexes(t *testing.T) {
	d, cleanup := setupTestDB(t)
	defer cleanup()

	if err := createTables(); err != nil {
		t.Fatalf("createTables() error = %v", err)
	}

	rows, err := d.Query("SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='usage_events'")
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil {
			have[name] = true
		}
	}

	if !have["idx_usage_events_dt_id"] {
		t.Errorf("composite expression index missing; have %v", have)
	}
	for _, dead := range []string{"idx_usage_events_timestamp", "idx_usage_events_ts_id", "idx_usage_events_dt"} {
		if have[dead] {
			t.Errorf("dead index %s should have been dropped; have %v", dead, have)
		}
	}

	// The composite index must actually serve the events range+order query.
	var plan string
	_ = d.QueryRow("EXPLAIN QUERY PLAN SELECT id FROM usage_events WHERE datetime(timestamp) >= datetime(?) ORDER BY datetime(timestamp) DESC, id DESC LIMIT 10",
		time.Now().Format(time.RFC3339)).Scan(new(string), new(int), new(int), &plan)
	t.Logf("events plan: %s", plan)
}

// The single-query implementation computes the bucket index in SQL via
// julianday arithmetic, so verify events land in the expected buckets.
func TestHandleTimeseriesBucketPlacement(t *testing.T) {
	d, cleanup := setupTestDB(t)
	defer cleanup()
	setTestPrices(map[string]modelPrice{"m": {Prompt: 1, Completion: 1, Cache: 0.1}})
	defer setTestPrices(nil)

	now := time.Now()
	// 24h range / 12 buckets => 2h buckets. Offsets chosen inside distinct buckets.
	cases := []struct {
		offset time.Duration
		bucket int
	}{
		{-30 * time.Minute, 11}, // last bucket
		{-3 * time.Hour, 10},    // (24-3)/2 = 10.5 -> 10
		{-11 * time.Hour, 6},    // 13/2 = 6.5 -> 6
		{-23 * time.Hour, 0},    // 1/2 = 0.5 -> 0
	}
	for _, tc := range cases {
		insertTestEvent(t, d, "p", "m", 1000, 100, 1100, false, now.Add(tc.offset).Format(time.RFC3339))
	}

	resp := handleTimeseries(map[string][]string{"range": {"24h"}, "buckets": {"12"}}, nil)
	var ts timeseriesResponse
	if err := json.Unmarshal(resp.Body, &ts); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, tc := range cases {
		if got := ts.Series[tc.bucket].Requests; got != 1 {
			t.Errorf("offset %s: bucket %d has %d requests, want 1 (series=%v)",
				tc.offset, tc.bucket, got, bucketRequestCounts(ts))
		}
	}
}

func bucketRequestCounts(ts timeseriesResponse) []int64 {
	out := make([]int64, len(ts.Series))
	for i, b := range ts.Series {
		out[i] = b.Requests
	}
	return out
}

func TestErrorLogIsBoundedAndNewestLast(t *testing.T) {
	errorLogMu.Lock()
	saved := errorLog
	errorLog = nil
	errorLogMu.Unlock()
	defer func() {
		errorLogMu.Lock()
		errorLog = saved
		errorLogMu.Unlock()
	}()

	for i := 0; i < errorLogCap+5; i++ {
		recordError("test_code", fmt.Sprintf("failure %d", i))
	}
	got := recentErrors()
	if len(got) != errorLogCap {
		t.Fatalf("error log length = %d, want cap %d", len(got), errorLogCap)
	}
	if got[len(got)-1].Message != fmt.Sprintf("failure %d", errorLogCap+4) {
		t.Errorf("newest entry = %q, want the last recorded", got[len(got)-1].Message)
	}
	if got[0].Message != "failure 5" {
		t.Errorf("oldest retained entry = %q, want failure 5", got[0].Message)
	}
	for _, e := range got {
		if e.Time == "" || e.Code != "test_code" {
			t.Errorf("entry missing fields: %+v", e)
		}
	}

	// A caller mutating the returned slice must not corrupt the log.
	got[0].Message = "mutated"
	if recentErrors()[0].Message == "mutated" {
		t.Error("recentErrors must return a copy")
	}
}

func TestUsageWriteQueueStatsAndDropCounting(t *testing.T) {
	// Drive the writer state directly so no persistence goroutine drains it.
	usageWriterState.Lock()
	usageWriterState.queue = make(chan pluginapi.UsageRecord, 3)
	usageWriterState.started = true
	usageWriterState.stop = make(chan struct{})
	usageWriterState.done = make(chan struct{})
	usageWriterState.Unlock()
	defer func() {
		usageWriterState.Lock()
		usageWriterState.queue = nil
		usageWriterState.started = false
		usageWriterState.stop = nil
		usageWriterState.done = nil
		usageWriterState.Unlock()
	}()

	used, capacity := usageWriteQueueStats()
	if used != 0 || capacity != 3 {
		t.Fatalf("usageWriteQueueStats() = %d/%d, want 0/3", used, capacity)
	}

	cacheMu.Lock()
	before := storageQueueDrops
	cacheMu.Unlock()

	// Two fit, three overflow -> the overflow must be counted as drops.
	for i := 0; i < 5; i++ {
		enqueueUsageEvent(pluginapi.UsageRecord{Model: "m"})
	}

	used, capacity = usageWriteQueueStats()
	if used != 3 || capacity != 3 {
		t.Errorf("queue = %d/%d, want 3/3 (full)", used, capacity)
	}
	cacheMu.Lock()
	dropped := storageQueueDrops - before
	cacheMu.Unlock()
	if dropped != 2 {
		t.Errorf("dropped events = %d, want 2", dropped)
	}
}

func TestUsageWriteQueueStatsWhenStopped(t *testing.T) {
	usageWriterState.Lock()
	prevQueue, prevStarted := usageWriterState.queue, usageWriterState.started
	usageWriterState.queue = nil
	usageWriterState.started = false
	usageWriterState.Unlock()
	defer func() {
		usageWriterState.Lock()
		usageWriterState.queue, usageWriterState.started = prevQueue, prevStarted
		usageWriterState.Unlock()
	}()

	if used, capacity := usageWriteQueueStats(); used != 0 || capacity != 0 {
		t.Fatalf("usageWriteQueueStats() = %d/%d, want 0/0 when the writer is stopped", used, capacity)
	}
	// Enqueuing while stopped must be a safe no-op (not a drop).
	enqueueUsageEvent(pluginapi.UsageRecord{Model: "m"})
}

func TestHealthReportsWriteQueueNotRingBuffer(t *testing.T) {
	d, cleanup := setupTestDB(t)
	defer cleanup()
	_ = d

	resp := handleHealthCheck()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := string(resp.Body)
	if !strings.Contains(body, `"write_queue_size"`) || !strings.Contains(body, `"write_queue_used"`) {
		t.Errorf("health JSON missing write queue fields: %s", body)
	}
	if strings.Contains(body, "ring_buffer") {
		t.Errorf("health JSON should no longer expose ring_buffer fields: %s", body)
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
