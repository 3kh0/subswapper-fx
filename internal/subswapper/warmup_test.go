package subswapper

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWarmupWindows(t *testing.T) {
	now := time.Now().UTC()
	claude := ServiceConfig{Name: "claude", Kind: "claude", AccountMode: AccountModeHome}
	codex := ServiceConfig{Name: "codex", Kind: "codex", AccountMode: AccountModeHome}
	claudeUsage := func(fiveHourReset, weeklyReset time.Time, pct float64) UsageSnapshot {
		return UsageSnapshot{
			FiveHour:      LimitWindow{Pct: PtrFloat64(pct), ResetsAt: fiveHourReset},
			Weekly:        LimitWindow{Pct: PtrFloat64(pct), ResetsAt: weeklyReset},
			ObservedAt:    now.Add(-6 * time.Hour),
			Source:        claudeUsageSourceProxy,
			TokenRevision: "rev",
		}
	}
	codexUsage := func(pct float64, reset time.Time, observed time.Time) UsageSnapshot {
		return UsageSnapshot{
			Weekly:     LimitWindow{Pct: PtrFloat64(pct), ResetsAt: reset},
			ObservedAt: observed,
		}
	}
	tests := []struct {
		name    string
		service ServiceConfig
		account AccountState
		want    []string
	}{
		{
			name:    "claude five-hour window reset while idle",
			service: claude,
			account: AccountState{SetupTokenRevision: "rev", ProxyUsage: claudeUsage(now.Add(-time.Hour), now.Add(48*time.Hour), 40)},
			want:    []string{"5h"},
		},
		{
			name:    "claude both windows reset while idle",
			service: claude,
			account: AccountState{SetupTokenRevision: "rev", ProxyUsage: claudeUsage(now.Add(-time.Hour), now.Add(-time.Minute), 40)},
			want:    []string{"5h", "weekly"},
		},
		{
			name:    "claude windows running",
			service: claude,
			account: AccountState{SetupTokenRevision: "rev", ProxyUsage: claudeUsage(now.Add(time.Hour), now.Add(48*time.Hour), 0)},
		},
		{
			name:    "claude reset passed but warmed within the window",
			service: claude,
			account: AccountState{SetupTokenRevision: "rev", WarmupAt: now.Add(-time.Hour), ProxyUsage: claudeUsage(now.Add(-time.Hour), now.Add(48*time.Hour), 40)},
		},
		{
			name:    "claude sample from an older token revision is unknown usage",
			service: claude,
			account: AccountState{SetupTokenRevision: "new", ProxyUsage: claudeUsage(now.Add(time.Hour), now.Add(48*time.Hour), 40)},
			want:    []string{WarmupReasonUnknownUsage},
		},
		{
			name:    "claude unknown usage warmed recently",
			service: claude,
			account: AccountState{SetupTokenRevision: "rev", WarmupAt: now.Add(-time.Hour)},
		},
		{
			name:    "claude without a setup token",
			service: claude,
			account: AccountState{ProxyUsage: claudeUsage(now.Add(-time.Hour), now.Add(-time.Hour), 40)},
		},
		{
			name:    "claude exhausted weekly window",
			service: claude,
			account: AccountState{SetupTokenRevision: "rev", ProxyUsage: func() UsageSnapshot {
				usage := claudeUsage(now.Add(-time.Hour), now.Add(48*time.Hour), 40)
				usage.Weekly.Pct = PtrFloat64(100)
				return usage
			}()},
		},
		{
			name:    "claude fable window reset after a haiku warm-up",
			service: claude,
			account: AccountState{SetupTokenRevision: "rev", WarmupAt: now.Add(-time.Hour), ProxyUsage: withFable(
				claudeUsage(now.Add(4*time.Hour), now.Add(6*24*time.Hour), 0), 32, now.Add(-3*time.Hour))},
			want: []string{warmupWindowFableWeekly},
		},
		{
			name:    "claude fable window warmed within the week",
			service: claude,
			account: AccountState{SetupTokenRevision: "rev", FableWarmupAt: now.Add(-24 * time.Hour), ProxyUsage: withFable(
				claudeUsage(now.Add(4*time.Hour), now.Add(6*24*time.Hour), 0), 32, now.Add(-3*time.Hour))},
		},
		{
			name:    "claude all three windows reset",
			service: claude,
			account: AccountState{SetupTokenRevision: "rev", ProxyUsage: withFable(
				claudeUsage(now.Add(-time.Hour), now.Add(-time.Hour), 40), 32, now.Add(-time.Hour))},
			want: []string{warmupWindowFiveHour, warmupWindowWeekly, warmupWindowFableWeekly},
		},
		{
			name:    "claude exhausted fable window still warms the five-hour window",
			service: claude,
			account: AccountState{SetupTokenRevision: "rev", ProxyUsage: withFable(
				claudeUsage(now.Add(-time.Hour), now.Add(48*time.Hour), 40), 100, now.Add(48*time.Hour))},
			want: []string{warmupWindowFiveHour},
		},
		{
			name:    "rejected credentials",
			service: claude,
			account: AccountState{SetupTokenRevision: "rev", CredentialsError: "setup token authentication rejected", ProxyUsage: claudeUsage(now.Add(-time.Hour), now.Add(-time.Hour), 40)},
		},
		{
			name:    "failed warm-up backing off",
			service: claude,
			account: AccountState{SetupTokenRevision: "rev", WarmupRetryAt: now.Add(time.Minute), ProxyUsage: claudeUsage(now.Add(-time.Hour), now.Add(-time.Hour), 40)},
		},
		{
			name:    "codex window floating a full week ahead",
			service: codex,
			account: AccountState{Usage: codexUsage(0, now.Add(weeklyWindowLength-10*time.Second), now.Add(-10*time.Second))},
			want:    []string{"weekly"},
		},
		{
			name:    "codex window started a while ago",
			service: codex,
			account: AccountState{Usage: codexUsage(0, now.Add(weeklyWindowLength-time.Hour), now)},
		},
		{
			name:    "codex window with usage is running",
			service: codex,
			account: AccountState{Usage: codexUsage(3, now.Add(weeklyWindowLength), now)},
		},
		{
			name:    "codex window without a reset",
			service: codex,
			account: AccountState{Usage: codexUsage(0, time.Time{}, now)},
			want:    []string{"weekly"},
		},
		{
			name:    "codex floating window warmed recently",
			service: codex,
			account: AccountState{WarmupAt: now.Add(-time.Minute), Usage: codexUsage(0, now.Add(weeklyWindowLength), now)},
		},
		{
			name:    "codex without usage",
			service: codex,
			account: AccountState{},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := warmupWindows(test.service, test.account, now)
			if !slices.Equal(got, test.want) {
				t.Fatalf("warmupWindows = %q, want %q", got, test.want)
			}
		})
	}
}

func withFable(usage UsageSnapshot, pct float64, reset time.Time) UsageSnapshot {
	usage.FableWeekly = LimitWindow{Pct: PtrFloat64(pct), ResetsAt: reset}
	return usage
}

func TestWarmupStartsIdleFableWindowWithFableModel(t *testing.T) {
	upstream := newProxyUpstream(t)
	cfg, _ := setupProxyAccounts(t, upstream.server.URL)
	upstream.respond("setup-token-b", func(w http.ResponseWriter, r *http.Request) {
		rateLimitHeaders(w, 0, 0.2, "allowed")
		w.Header().Set("anthropic-ratelimit-unified-7d_oi-utilization", "0")
		w.Header().Set("anthropic-ratelimit-unified-7d_oi-reset", strconv.FormatInt(time.Now().Add(weeklyWindowLength).Unix(), 10))
		w.WriteHeader(http.StatusOK)
	})

	// a is running everywhere; b was Haiku-warmed, but its Fable window
	// reset and only a Fable request starts it.
	now := time.Now().UTC()
	state, err := LoadState(cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	for name, fableReset := range map[string]time.Time{"a": now.Add(48 * time.Hour), "b": now.Add(-time.Hour)} {
		account := state.Service("claude").Accounts[name]
		account.ProxyUsage = UsageSnapshot{
			FiveHour:      LimitWindow{Pct: PtrFloat64(0), ResetsAt: now.Add(4 * time.Hour)},
			Weekly:        LimitWindow{Pct: PtrFloat64(20), ResetsAt: now.Add(72 * time.Hour)},
			FableWeekly:   LimitWindow{Pct: PtrFloat64(30), ResetsAt: fableReset},
			ObservedAt:    now.Add(-time.Hour),
			Source:        claudeUsageSourceProxy,
			TokenRevision: account.SetupTokenRevision,
		}
		account.WarmupAt = now.Add(-time.Hour)
		state.Service("claude").Accounts[name] = account
	}
	if err := SaveState(cfg.StatePath, state); err != nil {
		t.Fatal(err)
	}

	events, err := WarmupOnce(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Account != "b" || events[0].Err != nil ||
		!slices.Equal(events[0].Windows, []string{warmupWindowFableWeekly}) {
		t.Fatalf("events = %#v", events)
	}
	calls := upstream.recorded()
	if len(calls) != 1 || !strings.Contains(calls[0].Body, `"model":"`+defaultClaudeFableWarmupModel+`"`) {
		t.Fatalf("upstream calls = %#v", calls)
	}
	state, err = LoadState(cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	b := state.Service("claude").Accounts["b"]
	if b.FableWarmupAt.IsZero() || !b.FableWarmupAt.Equal(b.WarmupAt) {
		t.Fatalf("fable warm-up not recorded: fable=%s warmup=%s", b.FableWarmupAt, b.WarmupAt)
	}
	if !b.ProxyUsage.FableWeekly.ResetsAt.After(now) {
		t.Fatalf("fable window not recorded from the response: %#v", b.ProxyUsage.FableWeekly)
	}
	if events, err := WarmupOnce(context.Background(), cfg); err != nil || len(events) != 0 {
		t.Fatalf("second warm-up = %#v, %v", events, err)
	}
}

func TestWarmupStartsIdleClaudeAccountOnce(t *testing.T) {
	upstream := newProxyUpstream(t)
	// Warm-up is on by default.
	cfg, _ := setupProxyAccounts(t, upstream.server.URL)
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(forbidden.Close)
	useClaudeUsageServer(t, forbidden.URL)
	upstream.respond("setup-token-b", func(w http.ResponseWriter, r *http.Request) {
		rateLimitHeaders(w, 0, 0.2, "allowed")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"type":"message"}`))
	})

	// a is running; b's five-hour window reset while nothing used it.
	now := time.Now().UTC()
	state, err := LoadState(cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	for name, fiveHourReset := range map[string]time.Time{"a": now.Add(time.Hour), "b": now.Add(-time.Hour)} {
		account := state.Service("claude").Accounts[name]
		account.ProxyUsage = UsageSnapshot{
			FiveHour:      LimitWindow{Pct: PtrFloat64(30), ResetsAt: fiveHourReset},
			Weekly:        LimitWindow{Pct: PtrFloat64(20), ResetsAt: now.Add(72 * time.Hour)},
			ObservedAt:    now.Add(-5 * time.Hour),
			Source:        claudeUsageSourceProxy,
			TokenRevision: account.SetupTokenRevision,
		}
		state.Service("claude").Accounts[name] = account
	}
	if err := SaveState(cfg.StatePath, state); err != nil {
		t.Fatal(err)
	}

	cycle := MonitorOnce(context.Background(), cfg, false)
	if len(cycle.Errors) != 0 {
		t.Fatalf("cycle errors = %v", cycle.Errors)
	}
	if len(cycle.Warmups) != 1 || cycle.Warmups[0].Account != "b" || cycle.Warmups[0].Err != nil ||
		!slices.Equal(cycle.Warmups[0].Windows, []string{"5h"}) {
		t.Fatalf("warmups = %#v", cycle.Warmups)
	}
	calls := upstream.recorded()
	if len(calls) != 1 || calls[0].Path != "/v1/messages" || calls[0].Authorization != "Bearer setup-token-b" {
		t.Fatalf("upstream calls = %#v", calls)
	}
	var body struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
	}
	if err := json.Unmarshal([]byte(calls[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	if body.Model != defaultClaudeWarmupModel || body.MaxTokens != 1 {
		t.Fatalf("warm-up body = %s", calls[0].Body)
	}

	state, err = LoadState(cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	b := state.Service("claude").Accounts["b"]
	if b.WarmupAt.IsZero() || !b.WarmupRetryAt.IsZero() {
		t.Fatalf("warm-up not recorded: at=%s retry=%s", b.WarmupAt, b.WarmupRetryAt)
	}
	if !b.ProxyUsage.FiveHour.ResetsAt.After(now) || b.ProxyUsage.Weekly.Pct == nil || *b.ProxyUsage.Weekly.Pct != 20 {
		t.Fatalf("warm-up response headers not recorded: %#v", b.ProxyUsage)
	}
	if state.Service("claude").ActiveAccount != "a" {
		t.Fatalf("warm-up changed the route to %q", state.Service("claude").ActiveAccount)
	}

	cycle = MonitorOnce(context.Background(), cfg, false)
	if len(cycle.Warmups) != 0 || len(upstream.recorded()) != 1 {
		t.Fatalf("second cycle warmed again: %#v", cycle.Warmups)
	}
}

func TestWarmupDisabledSendsNothing(t *testing.T) {
	upstream := newProxyUpstream(t)
	cfg, _ := setupProxyAccounts(t, upstream.server.URL)
	disabled := false
	cfg.Monitor.Warmup = &disabled
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(forbidden.Close)
	useClaudeUsageServer(t, forbidden.URL)

	cycle := MonitorOnce(context.Background(), cfg, false)
	if len(cycle.Warmups) != 0 || len(upstream.recorded()) != 0 {
		t.Fatalf("warm-up ran while disabled: %#v", cycle.Warmups)
	}
}

func TestWarmupRejectedClaudeTokenBacksOff(t *testing.T) {
	upstream := newProxyUpstream(t)
	cfg, _ := setupProxyAccounts(t, upstream.server.URL)
	upstream.respond("setup-token-a", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	upstream.respond("setup-token-b", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	events, err := WarmupOnce(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Err == nil || events[1].Err == nil {
		t.Fatalf("events = %#v", events)
	}
	state, err := LoadState(cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	a, b := state.Service("claude").Accounts["a"], state.Service("claude").Accounts["b"]
	if a.CredentialsError == "" {
		t.Fatal("a rejected token was not recorded")
	}
	if !b.WarmupAt.IsZero() || !b.WarmupRetryAt.After(time.Now()) {
		t.Fatalf("b failure did not back off: at=%s retry=%s", b.WarmupAt, b.WarmupRetryAt)
	}
	events, err = WarmupOnce(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 || len(upstream.recorded()) != 2 {
		t.Fatalf("retried inside the backoff: %#v", events)
	}
}

func TestWarmupRunsEphemeralCodexTurnInAccountHome(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	fakeCodex := filepath.Join(dir, "codex")
	script := `#!/bin/sh
printf '%s\n' "$CODEX_HOME" "$@" > "` + argsFile + `"
if [ "$(basename "$CODEX_HOME")" = "broken" ]; then
	echo "starting" >&2
	echo "ERROR: unexpected status 401 Unauthorized" >&2
	exit 1
fi
echo OK
`
	if err := os.WriteFile(fakeCodex, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	oldCommand := codexCommand
	codexCommand = fakeCodex
	t.Cleanup(func() { codexCommand = oldCommand })

	cfg := Config{
		BackupRoot: filepath.Join(dir, "accounts"),
		StatePath:  filepath.Join(dir, "state.json"),
		Services: []ServiceConfig{{
			Name: "codex", Kind: "codex", AccountMode: AccountModeHome, WarmupModel: "cheap-model",
		}},
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	state := NewState()
	for _, name := range []string{"work", "broken"} {
		home := AccountDir(cfg, "codex", name)
		if err := os.MkdirAll(home, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"access","account_id":"`+name+`"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		state.Service("codex").Accounts[name] = AccountState{
			Name:    name,
			AddedAt: now,
			Usage: UsageSnapshot{
				Weekly:     LimitWindow{Pct: PtrFloat64(0), ResetsAt: now.Add(weeklyWindowLength)},
				ObservedAt: now,
			},
		}
	}
	if err := SaveState(cfg.StatePath, state); err != nil {
		t.Fatal(err)
	}

	events, err := WarmupOnce(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Account != "broken" || events[1].Account != "work" {
		t.Fatalf("events = %#v", events)
	}
	if events[0].Err == nil || !strings.Contains(events[0].Err.Error(), "401 Unauthorized") {
		t.Fatalf("broken account error = %v", events[0].Err)
	}
	if events[1].Err != nil {
		t.Fatalf("work account error = %v", events[1].Err)
	}
	recorded, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(recorded)), "\n")
	if args[0] != AccountDir(cfg, "codex", "work") {
		t.Fatalf("CODEX_HOME = %q", args[0])
	}
	for _, want := range []string{"exec", "--ephemeral", "--ignore-user-config", "cheap-model", warmupPrompt} {
		if !slices.Contains(args[1:], want) {
			t.Fatalf("codex args %q missing %q", args[1:], want)
		}
	}

	state, err = LoadState(cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	if work := state.Service("codex").Accounts["work"]; work.WarmupAt.IsZero() {
		t.Fatal("work warm-up not recorded")
	}
	if broken := state.Service("codex").Accounts["broken"]; !broken.WarmupAt.IsZero() || broken.WarmupRetryAt.IsZero() {
		t.Fatalf("broken warm-up state: at=%s retry=%s", broken.WarmupAt, broken.WarmupRetryAt)
	}
	if candidates, err := PlanWarmups(context.Background(), cfg); err != nil || len(candidates) != 0 {
		t.Fatalf("candidates after warm-up = %#v, %v", candidates, err)
	}
}

func TestWarmupModelValidation(t *testing.T) {
	for _, test := range []struct {
		name    string
		service ServiceConfig
		ok      bool
	}{
		{"claude home", ServiceConfig{Name: "claude", Kind: "claude", WarmupModel: "claude-haiku-4-5"}, true},
		{"flag-like model", ServiceConfig{Name: "codex", Kind: "codex", WarmupModel: "--yolo"}, false},
		{"padded model", ServiceConfig{Name: "codex", Kind: "codex", WarmupModel: " gpt "}, false},
		{"custom kind", ServiceConfig{Name: "other", Kind: "custom", WarmupModel: "m", Files: []ManagedFile{{Path: "/tmp/x", BackupName: "x"}}}, false},
		{"claude fable model", ServiceConfig{Name: "claude", Kind: "claude", WarmupFableModel: "claude-fable-5-1"}, true},
		{"codex fable model", ServiceConfig{Name: "codex", Kind: "codex", WarmupFableModel: "claude-fable-5-1"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := Config{Services: []ServiceConfig{test.service}}
			cfg.ApplyDefaults()
			if err := cfg.Validate(); (err == nil) != test.ok {
				t.Fatalf("Validate() = %v, want ok=%v", err, test.ok)
			}
		})
	}
}
