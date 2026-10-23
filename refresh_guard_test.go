package main

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestBeginRefreshDeduplicatesConcurrentClicks(t *testing.T) {
	key := refreshKey("test-provider", "acct-dup")
	endRefresh(key) // ensure a clean slate

	if !beginRefresh(key) {
		t.Fatal("first call should win")
	}
	// Repeated clicks while the first fetch runs must not queue more work.
	for i := 0; i < 5; i++ {
		if beginRefresh(key) {
			t.Fatal("duplicate refresh was allowed")
		}
	}
	if !refreshRunning("test-provider", "acct-dup") {
		t.Error("in-flight flag not visible while running")
	}
	endRefresh(key)
	if refreshRunning("test-provider", "acct-dup") {
		t.Error("in-flight flag not cleared")
	}
	if !beginRefresh(key) {
		t.Error("refresh should be possible again after completion")
	}
	endRefresh(key)
}

func TestBeginRefreshIsKeyedPerProviderAndAccount(t *testing.T) {
	a := refreshKey("newapi", "one")
	b := refreshKey("newapi", "two")
	c := refreshKey("ollama", "one")
	for _, k := range []string{a, b, c} {
		endRefresh(k)
	}
	if !beginRefresh(a) || !beginRefresh(b) || !beginRefresh(c) {
		t.Fatal("distinct accounts/providers must not block each other")
	}
	for _, k := range []string{a, b, c} {
		endRefresh(k)
	}
}

func TestBeginRefreshIsSafeUnderConcurrency(t *testing.T) {
	key := refreshKey("test-provider", "race")
	endRefresh(key)

	var winners int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if beginRefresh(key) {
				atomic.AddInt32(&winners, 1)
			}
		}()
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("winners = %d, want exactly 1", winners)
	}
	endRefresh(key)
}
