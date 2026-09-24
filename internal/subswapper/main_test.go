package subswapper

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// TestMain keeps warm-up, which is on by default, away from the real
// Anthropic API and the installed Codex CLI, and fails the run if a test
// that did not ask for a warm-up sent one.
func TestMain(m *testing.M) {
	var strayClaude atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		strayClaude.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	claudeWarmupUpstream = upstream.URL
	dir, err := os.MkdirTemp("", "subswapper-test-*")
	if err != nil {
		panic(err)
	}
	strayCodex := filepath.Join(dir, "codex-exec-called")
	codexCommand = filepath.Join(dir, "codex")
	script := "#!/bin/sh\n[ \"$1\" = exec ] && touch '" + strayCodex + "'\nexit 1\n"
	if err := os.WriteFile(codexCommand, []byte(script), 0o700); err != nil {
		panic(err)
	}
	code := m.Run()
	upstream.Close()
	if strayClaude.Load() > 0 {
		fmt.Fprintf(os.Stderr, "%d unexpected Claude warm-up request(s)\n", strayClaude.Load())
		code = 1
	}
	if _, err := os.Stat(strayCodex); err == nil {
		fmt.Fprintln(os.Stderr, "unexpected Codex warm-up run")
		code = 1
	}
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
