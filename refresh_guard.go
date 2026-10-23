package main

import "sync"

// Manual "refresh quota" endpoints start a background fetch and return
// immediately. Without a guard, holding down or repeatedly clicking the button
// spawns one goroutine per click, each firing an upstream request with the same
// credentials and racing to write the cache. That both hammers the provider and
// makes the eventual result look random.
//
// refreshInFlight tracks one key per (provider, account). beginRefresh reports
// false when a refresh for that key is already running, so callers can skip the
// duplicate instead of queueing it.
var (
	refreshInFlightMu sync.Mutex
	refreshInFlight   = make(map[string]bool)
)

func refreshKey(provider, account string) string {
	return provider + "|" + account
}

func beginRefresh(key string) bool {
	refreshInFlightMu.Lock()
	defer refreshInFlightMu.Unlock()
	if refreshInFlight[key] {
		return false
	}
	refreshInFlight[key] = true
	return true
}

func endRefresh(key string) {
	refreshInFlightMu.Lock()
	defer refreshInFlightMu.Unlock()
	delete(refreshInFlight, key)
}

// refreshRunning reports whether a manual refresh is in flight. Handlers expose
// this so the dashboard can keep a spinner up until the cache really changed.
func refreshRunning(provider, account string) bool {
	refreshInFlightMu.Lock()
	defer refreshInFlightMu.Unlock()
	return refreshInFlight[refreshKey(provider, account)]
}
