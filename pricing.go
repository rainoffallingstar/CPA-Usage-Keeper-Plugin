package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type modelPrice struct {
	Prompt     float64 `json:"prompt"`
	Completion float64 `json:"completion"`
	Cache      float64 `json:"cache"`
}

type pricesResponse struct {
	Prices map[string]modelPrice `json:"prices"`
}

var pricesMu sync.RWMutex
var pricesStore = make(map[string]modelPrice)

func init() {
	pricesMu.Lock()
	defer pricesMu.Unlock()
	pricesStore = make(map[string]modelPrice)
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
	pricesStore[payload.Model] = payload.Price
	pricesMu.Unlock()
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
	case "delete":
		if model == "" {
			return jsonResponse(http.StatusBadRequest, map[string]string{"error": "model is required"})
		}
		pricesMu.Lock()
		delete(pricesStore, model)
		pricesMu.Unlock()
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
		pricesMu.Lock()
		pricesStore[model] = req.Price
		pricesMu.Unlock()
	case "delete":
		pricesMu.Lock()
		delete(pricesStore, model)
		pricesMu.Unlock()
	default:
		return jsonResponse(http.StatusBadRequest, map[string]string{"error": "unknown action, use 'set' or 'delete'"})
	}
	return handleGetPrices()
}

func computeCost(model string, inputTokens, outputTokens, cachedTokens int64) float64 {
	pricesMu.RLock()
	price, ok := pricesStore[model]
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
