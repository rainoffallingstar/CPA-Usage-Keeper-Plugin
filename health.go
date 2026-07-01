package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type healthAlert struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

type healthResponse struct {
	Status  string              `json:"status"`
	Alerts  []healthAlert       `json:"alerts"`
	Runtime runtimeHealthStatus `json:"runtime"`
	Storage storageHealthStatus `json:"storage"`
}

type runtimeHealthStatus struct {
	UptimeSeconds        int64   `json:"uptime_seconds"`
	TotalRequests        int64   `json:"total_requests"`
	RingBufferSize       int     `json:"ring_buffer_size"`
	RingBufferUsed       int     `json:"ring_buffer_used"`
	SummaryCacheHitRate  float64 `json:"summary_cache_hit_rate"`
	EventsCacheHitRate   float64 `json:"events_cache_hit_rate"`
	LastWriteDurationMs  int64   `json:"last_write_duration_ms"`
	StorageWriteErrors   int64   `json:"storage_write_errors"`
}

type storageHealthStatus struct {
	DBPath      string `json:"db_path"`
	DBFileSize  int64  `json:"db_file_size"`
	WALFileSize int64  `json:"wal_file_size"`
	Status      string `json:"status"`
	LastError   string `json:"last_error,omitempty"`
}

var (
	startTime         = time.Now()
	summaryCacheHits   int64
	summaryCacheMisses int64
	eventsCacheHits    int64
	eventsCacheMisses  int64
	storageErrCount    int64
	lastWriteMs        int64
	cacheMu            sync.Mutex
	dashboardVersion   uint64
)

func handleHealthCheck() pluginapi.ManagementResponse {
	cfg := currentConfig()
	dbPath := cfg.DBPath
	var totalEvents int64
	dbMu.RLock()
	d := db
	if d != nil {
		_ = d.QueryRow("SELECT COUNT(*) FROM usage_events").Scan(&totalEvents)
	}
	dbMu.RUnlock()

	ringMu.RLock()
	ringUsed := ringCount
	ringCap := len(ringBuf)
	ringMu.RUnlock()

	cacheMu.Lock()
	sumHits := summaryCacheHits
	sumMisses := summaryCacheMisses
	evtHits := eventsCacheHits
	evtMisses := eventsCacheMisses
	lastWrite := lastWriteMs
	storeErrs := storageErrCount
	cacheMu.Unlock()

	alerts := make([]healthAlert, 0)
	if storeErrs > 0 {
		alerts = append(alerts, healthAlert{Severity: "error", Code: "storage_write_errors", Message: fmt.Sprintf("%d storage write errors detected", storeErrs)})
	}
	if lastWrite > 1000 {
		alerts = append(alerts, healthAlert{Severity: "warn", Code: "storage_writer_slow", Message: fmt.Sprintf("Last write took %dms", lastWrite)})
	}

	status := "ok"
	for _, a := range alerts {
		if a.Severity == "error" {
			status = "error"
			break
		} else if a.Severity == "warn" {
			status = "warn"
		}
	}

	return jsonResponse(http.StatusOK, healthResponse{
		Status: status,
		Alerts: alerts,
		Runtime: runtimeHealthStatus{
			UptimeSeconds:        int64(time.Since(startTime).Seconds()),
			TotalRequests:        totalEvents,
			RingBufferSize:       ringCap,
			RingBufferUsed:       ringUsed,
			SummaryCacheHitRate:  hitRate(sumHits, sumMisses),
			EventsCacheHitRate:   hitRate(evtHits, evtMisses),
			LastWriteDurationMs:  lastWrite,
			StorageWriteErrors:   storeErrs,
		},
		Storage: storageHealthStatus{
			DBPath:      dbPath,
			DBFileSize:  fileSize(dbPath),
			WALFileSize: fileSize(dbPath + "-wal"),
			Status:      "connected",
		},
	})
}

func hitRate(hits, misses int64) float64 {
	total := hits + misses
	if total == 0 {
		return 0
	}
	return float64(hits) / float64(total) * 100
}

func fileSize(path string) int64 {
	if path == "" {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func dashboardWeakETag(parts ...string) string {
	h := sha256.New224()
	h.Write([]byte(strconv.FormatUint(dashboardVersion, 10)))
	for _, p := range parts {
		h.Write([]byte(p))
	}
	return fmt.Sprintf(`W/"%x"`, h.Sum(nil)[:8])
}

func notModifiedResponse(etag string) pluginapi.ManagementResponse {
	return pluginapi.ManagementResponse{StatusCode: http.StatusNotModified, Headers: map[string][]string{"ETag": {etag}, "Cache-Control": {"private, no-cache"}}, Body: nil}
}

func checkETag(headers map[string][]string, etag string) bool {
	if etag == "" {
		return false
	}
	if vals, ok := headers["If-None-Match"]; ok && len(vals) > 0 {
		return vals[0] == etag
	}
	return false
}
