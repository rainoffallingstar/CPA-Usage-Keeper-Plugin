package main

import (
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
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

type pluginConfig struct {
	DBPath            string `yaml:"db_path"`
	RetentionDays     int    `yaml:"retention_days"`
	MaxInMemoryEvents int    `yaml:"max_in_memory_events"`
	RefreshSeconds    int    `yaml:"refresh_seconds"`
	APIKeyHashSalt    string `yaml:"api_key_hash_salt"`
}

func defaultConfig() pluginConfig {
	return pluginConfig{
		DBPath:            defaultDBPath,
		RetentionDays:     defaultRetentionDays,
		MaxInMemoryEvents: defaultMaxInMemoryEvents,
		RefreshSeconds:    defaultRefreshSeconds,
	}
}

// envelope types
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

// API response types
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
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Requests     int64  `json:"requests"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	TotalTokens  int64  `json:"total_tokens"`
}
type usageEvent struct {
	ID           int64   `json:"id"`
	Timestamp    string  `json:"timestamp"`
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	LatencyMs    int64   `json:"latency_ms"`
	Failed       bool    `json:"failed"`
	CacheHitRate float64 `json:"cache_hit_rate"`
	FailureBody  string  `json:"failure_body,omitempty"`
	AuthID       string  `json:"auth_id"`
	ExecutorType string  `json:"executor_type"`
}
type eventsResponse struct {
	Events []usageEvent `json:"events"`
	Total  int64        `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}
type quotioUsageResponse struct {
	Usage      quotioUsageData `json:"usage"`
	FailedReqs int64           `json:"failed_requests"`
}
type quotioUsageData struct {
	TotalRequests int64 `json:"total_requests"`
	SuccessCount  int64 `json:"success_count"`
	FailureCount  int64 `json:"failure_count"`
	TotalTokens   int64 `json:"total_tokens"`
	InputTokens   int64 `json:"input_tokens"`
	OutputTokens  int64 `json:"output_tokens"`
}
