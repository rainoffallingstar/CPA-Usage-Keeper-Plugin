package main

import (
	"os"
	"strings"
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

// TestEveryProviderRefreshPathRegistersInFlight is the regression guard for the
// Ollama bug: the dashboard polls `refreshing` to know when a refresh finished,
// so a refresh branch that starts the work without beginRefresh() makes the very
// first poll report "done" and re-render stale numbers. The user then has to
// click a second time, by which point the background fetch has landed.
//
// Rather than matching call shapes (a `go func(){…}` wrapper hid the call from an
// earlier version of this test), this walks every `case "refresh"` branch and
// requires the branch body to register the in-flight state.
func TestEveryProviderRefreshPathRegistersInFlight(t *testing.T) {
	files := []string{
		"opencode.go", "glmcoding.go", "deepseek.go",
		"ollama.go", "colab.go", "newapi.go",
	}

	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		lines := strings.Split(string(raw), "\n")

		branches := 0
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, `case "refresh"`) {
				continue
			}
			branches++
			// The branch body runs until the next `case` at any depth, or until a
			// closing brace that dedents past the case's indentation.
			caseIndent := len(line) - len(strings.TrimLeft(line, "\t"))
			guarded := false
			for j := i + 1; j < len(lines); j++ {
				l := lines[j]
				lTrim := strings.TrimSpace(l)
				indent := len(l) - len(strings.TrimLeft(l, "\t"))
				if strings.HasPrefix(lTrim, "case ") && indent <= caseIndent {
					break
				}
				if strings.HasPrefix(lTrim, "}") && indent < caseIndent {
					break
				}
				if strings.Contains(l, "beginRefresh") {
					guarded = true
					break
				}
				// A POST branch may delegate to the GET handler, which owns the
				// guard. Delegation is not a separate launch path.
				if strings.Contains(l, "handleColabQuotaGet(") || strings.Contains(l, "handleNewAPIQuotaGet(") {
					guarded = true
					break
				}
			}
			if !guarded {
				t.Errorf("%s:%d refresh branch does not call beginRefresh; the dashboard reports "+
					"completion on its first poll and shows stale values until a second click",
					file, i+1)
			}
		}
		if branches == 0 {
			t.Errorf("%s has no recognized refresh branch; the test needs updating for it", file)
		}
	}
}
