package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
)

// installTestDB swaps the global handle for the duration of a test.
func installTestDB(t *testing.T, d *sql.DB) {
	t.Helper()
	orig := db
	dbMu.Lock()
	db = d
	dbMu.Unlock()
	t.Cleanup(func() {
		dbMu.Lock()
		db = orig
		dbMu.Unlock()
	})
}

func TestHandleModelsReportsQueryFailureInsteadOfEmptyList(t *testing.T) {
	d, cleanup := setupTestDB(t)
	defer cleanup()

	// Close the handle so every query fails. Before the fix this path returned
	// 200 + [] and the dashboard rendered "no usage records in this range",
	// which is indistinguishable from a genuinely idle instance.
	d.Close()
	installTestDB(t, d)

	resp := handleModels(map[string][]string{"range": {"30d"}})
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 so the UI can show a real error", resp.StatusCode)
	}
	var body map[string]string
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["error"] == "" {
		t.Fatalf("missing error detail: %s", resp.Body)
	}
	if !hasRecentError("models_query_failed") {
		t.Error("query failure was not recorded for the health page")
	}
}

func TestCheckAndRepairIndexesLeavesHealthyDatabaseAlone(t *testing.T) {
	d, cleanup := setupTestDB(t)
	defer cleanup()
	installTestDB(t, d)

	if _, err := d.Exec("CREATE INDEX IF NOT EXISTS idx_usage_events_model ON usage_events(model)"); err != nil {
		t.Fatalf("index: %v", err)
	}
	now := "2026-10-19T00:00:00Z"
	for i := 0; i < 20; i++ {
		insertTestEvent(t, d, "p", "m", 10, 5, 15, false, now)
	}

	checkAndRepairIndexes(d)
	if ok, msg := dbIntegrityStatus(); !ok {
		t.Fatalf("healthy database reported corrupt: %s", msg)
	}
	var n int
	if err := d.QueryRow("SELECT COUNT(*) FROM usage_events").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 20 {
		t.Fatalf("rows = %d, want 20", n)
	}
	clearErrorsByCode("db_integrity")
	clearErrorsByCode("db_reindex_failed")
}

func TestHealthExposesIntegrityFlag(t *testing.T) {
	d, cleanup := setupTestDB(t)
	defer cleanup()
	installTestDB(t, d)
	clearErrorsByCode("db_integrity")
	clearErrorsByCode("db_reindex_failed")
	clearErrorsByCode("db_integrity_check_failed")

	resp := handleHealthCheck()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body healthResponse
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !body.Storage.IntegrityOK {
		t.Errorf("integrity_ok = false on a healthy db: %s", body.Storage.Integrity)
	}
}
