package main

import (
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	maxExportJobs        = 16
	maxConcurrentExports = 2
	exportPageSize       = 5000
	exportTempDir        = "usage-keeper-exports"
)

type exportJobStatus string

const (
	jobPending   exportJobStatus = "pending"
	jobRunning   exportJobStatus = "running"
	jobCompleted exportJobStatus = "completed"
	jobFailed    exportJobStatus = "failed"
)

type exportJob struct {
	ID            string          `json:"id"`
	Status        exportJobStatus `json:"status"`
	Format        string          `json:"format"`
	Gzip          bool            `json:"gzip"`
	CreatedAt     string          `json:"created_at"`
	CompletedAt   string          `json:"completed_at,omitempty"`
	TotalRecords  int             `json:"total_records"`
	ExportedSoFar int             `json:"exported_so_far"`
	FilePath      string          `json:"-"`
	Error         string          `json:"error,omitempty"`
}

type exportJobsResponse struct {
	Jobs []exportJob `json:"jobs"`
}

var (
	exportJobsMu   sync.Mutex
	exportJobsMap  = make(map[string]*exportJob)
	exportJobOrder []string
	exportRunning  int
	exportNextID   int64
)

func initExportDir() error {
	return os.MkdirAll(exportTempDir, 0700)
}

func handleCreateExportJob(query map[string][]string) pluginapi.ManagementResponse {
	exportJobsMu.Lock()
	defer exportJobsMu.Unlock()

	for len(exportJobOrder) >= maxExportJobs {
		oldID := exportJobOrder[0]
		exportJobOrder = exportJobOrder[1:]
		if job, ok := exportJobsMap[oldID]; ok {
			os.Remove(job.FilePath)
			delete(exportJobsMap, oldID)
		}
	}

	format := "json"
	if v, ok := query["format"]; ok && len(v) > 0 {
		switch strings.ToLower(v[0]) {
		case "csv": format = "csv"
		case "jsonl": format = "jsonl"
		}
	}
	gz := false
	if v, ok := query["gzip"]; ok && len(v) > 0 {
		gz = v[0] == "1" || v[0] == "true"
	}

	exportNextID++
	id := fmt.Sprintf("export-%d", exportNextID)
	initExportDir()
	fp := filepath.Join(exportTempDir, id+".tmp")

	job := &exportJob{
		ID: id, Status: jobPending, Format: format, Gzip: gz,
		CreatedAt: time.Now().UTC().Format(time.RFC3339), FilePath: fp,
	}
	exportJobsMap[id] = job
	exportJobOrder = append(exportJobOrder, id)

	if exportRunning < maxConcurrentExports {
		exportRunning++
		go runExportJob(job)
	}
	return jsonResponse(http.StatusOK, job)
}

func startPendingExport() {
	exportJobsMu.Lock()
	defer exportJobsMu.Unlock()
	for _, id := range exportJobOrder {
		if j, ok := exportJobsMap[id]; ok && j.Status == jobPending {
			exportRunning++
			go runExportJob(j)
			return
		}
	}
}

func runExportJob(job *exportJob) {
	defer func() {
		exportJobsMu.Lock()
		exportRunning--
		exportJobsMu.Unlock()
		startPendingExport()
	}()

	exportJobsMu.Lock()
	job.Status = jobRunning
	exportJobsMu.Unlock()

	dbMu.RLock()
	d := db
	dbMu.RUnlock()
	if d == nil {
		exportJobsMu.Lock()
		job.Status = jobFailed; job.Error = "database not available"
		exportJobsMu.Unlock()
		return
	}

	var total int
	d.QueryRow("SELECT COUNT(*) FROM usage_events").Scan(&total)
	job.TotalRecords = total

	f, err := os.Create(job.FilePath)
	if err != nil {
		exportJobsMu.Lock()
		job.Status = jobFailed; job.Error = err.Error()
		exportJobsMu.Unlock()
		return
	}
	defer f.Close()

	offset := 0
	first := true

	for {
		rows, err := d.Query(
			"SELECT id, timestamp, provider, model, input_tokens, output_tokens, total_tokens, latency_ms, failed, failure_body, auth_id, executor_type, cached_tokens FROM usage_events ORDER BY id ASC LIMIT ? OFFSET ?",
			exportPageSize, offset,
		)
		if err != nil {
			exportJobsMu.Lock()
			job.Status = jobFailed; job.Error = err.Error()
			exportJobsMu.Unlock()
			return
		}

		batch := 0
		if job.Format == "csv" {
			w := csv.NewWriter(f)
			w.Write([]string{"id","timestamp","provider","model","input_tokens","output_tokens","total_tokens","latency_ms","failed","failure_body","auth_id","executor_type"})
			for rows.Next() {
				var e usageEvent; var c int64
				if rows.Scan(&e.ID, &e.Timestamp, &e.Provider, &e.Model, &e.InputTokens, &e.OutputTokens, &e.TotalTokens, &e.LatencyMs, &e.Failed, &e.FailureBody, &e.AuthID, &e.ExecutorType, &c) == nil {
					w.Write([]string{strconv.FormatInt(e.ID,10), e.Timestamp, e.Provider, e.Model, strconv.FormatInt(e.InputTokens,10), strconv.FormatInt(e.OutputTokens,10), strconv.FormatInt(e.TotalTokens,10), strconv.FormatInt(e.LatencyMs,10), strconv.FormatBool(e.Failed), e.FailureBody, e.AuthID, e.ExecutorType})
					batch++
				}
			}
			w.Flush()
		} else if job.Format == "jsonl" {
			enc := json.NewEncoder(f)
			for rows.Next() {
				var e usageEvent; var c int64
				if rows.Scan(&e.ID, &e.Timestamp, &e.Provider, &e.Model, &e.InputTokens, &e.OutputTokens, &e.TotalTokens, &e.LatencyMs, &e.Failed, &e.FailureBody, &e.AuthID, &e.ExecutorType, &c) == nil {
					enc.Encode(e); batch++
				}
			}
		} else {
			if first { f.Write([]byte("{\"events\":[")) }
			for rows.Next() {
				var e usageEvent; var c int64
				if rows.Scan(&e.ID, &e.Timestamp, &e.Provider, &e.Model, &e.InputTokens, &e.OutputTokens, &e.TotalTokens, &e.LatencyMs, &e.Failed, &e.FailureBody, &e.AuthID, &e.ExecutorType, &c) == nil {
					if !first { f.Write([]byte(",")) }
					data, _ := json.Marshal(e); f.Write(data); batch++; first = false
				}
			}
		}
		rows.Close()
		offset += batch
		exportJobsMu.Lock()
		job.ExportedSoFar = offset
		exportJobsMu.Unlock()
		if batch < exportPageSize { break }
	}
	if job.Format == "json" { f.Write([]byte("]}")) }

	if job.Gzip {
		in, _ := os.ReadFile(job.FilePath)
		f2, _ := os.Create(job.FilePath + ".gz")
		w := gzip.NewWriter(f2)
		w.Write(in); w.Close(); f2.Close()
		os.Rename(job.FilePath+".gz", job.FilePath)
	}

	exportJobsMu.Lock()
	job.Status = jobCompleted
	job.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	exportJobsMu.Unlock()
}

func handleGetExportJobs(query map[string][]string) pluginapi.ManagementResponse {
	exportJobsMu.Lock()
	defer exportJobsMu.Unlock()
	jobs := make([]exportJob, 0)
	for _, id := range exportJobOrder {
		if j, ok := exportJobsMap[id]; ok { jobs = append(jobs, *j) }
	}
	return jsonResponse(http.StatusOK, exportJobsResponse{Jobs: jobs})
}

func handleGetExportDownload(query map[string][]string) pluginapi.ManagementResponse {
	id := ""
	if v, ok := query["id"]; ok && len(v) > 0 { id = v[0] }
	if id == "" { return jsonResponse(http.StatusBadRequest, map[string]string{"error":"missing id"}) }
	exportJobsMu.Lock()
	job, ok := exportJobsMap[id]
	exportJobsMu.Unlock()
	if !ok { return jsonResponse(http.StatusNotFound, map[string]string{"error":"job not found"}) }
	if job.Status != jobCompleted { return jsonResponse(http.StatusBadRequest, map[string]string{"error":"job not completed"}) }
	data, err := os.ReadFile(job.FilePath)
	if err != nil { return jsonResponse(http.StatusInternalServerError, map[string]string{"error":"read failed"}) }
	return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: map[string][]string{"Content-Type":{"application/octet-stream"}}, Body: data}
}

func handleDeleteExportJob(query map[string][]string) pluginapi.ManagementResponse {
	id := ""
	if v, ok := query["id"]; ok && len(v) > 0 { id = v[0] }
	if id == "" { return jsonResponse(http.StatusBadRequest, map[string]string{"error":"missing id"}) }
	exportJobsMu.Lock()
	defer exportJobsMu.Unlock()
	job, ok := exportJobsMap[id]
	if !ok { return jsonResponse(http.StatusNotFound, map[string]string{"error":"job not found"}) }
	os.Remove(job.FilePath)
	delete(exportJobsMap, id)
	for i, oid := range exportJobOrder {
		if oid == id { exportJobOrder = append(exportJobOrder[:i], exportJobOrder[i+1:]...); break }
	}
	return jsonResponse(http.StatusOK, map[string]string{"deleted":id})
}
