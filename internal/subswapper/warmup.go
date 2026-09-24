package subswapper

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// A subscription window starts on the first request after it resets, so an
// idle account's clock is not running. A warm-up sends the smallest request
// the provider accepts to start it; the reset then comes that much sooner
// than if the clock had waited for the next real use.

const (
	fiveHourWindowLength = 5 * time.Hour
	weeklyWindowLength   = 7 * 24 * time.Hour
	// warmupFloatTolerance separates an unstarted Codex window, whose reset
	// floats one full window after each observation, from a started one.
	warmupFloatTolerance = 2 * time.Minute
	// warmupRetryBackoff paces attempts after a failed warm-up.
	warmupRetryBackoff     = 15 * time.Minute
	claudeWarmupTimeout    = 30 * time.Second
	codexWarmupTimeout     = 90 * time.Second
	codexWarmupStderrLimit = 64 << 10

	defaultClaudeWarmupModel = "claude-haiku-4-5"
	// defaultClaudeFableWarmupModel starts the Fable weekly window, which
	// only Fable responses consume and report.
	defaultClaudeFableWarmupModel = "claude-fable-5-1"
	// claudeWarmupSystemPrompt is the identity Anthropic expects on requests
	// made with a Claude Code OAuth token.
	claudeWarmupSystemPrompt = "You are Claude Code, Anthropic's official CLI for Claude."
	warmupPrompt             = "Reply with OK."

	// WarmupReasonUnknownUsage marks a Claude account with no usage sample;
	// only a request reveals its windows.
	WarmupReasonUnknownUsage = "usage unknown"

	warmupWindowFiveHour    = "5h"
	warmupWindowWeekly      = "weekly"
	warmupWindowFableWeekly = "fable weekly"
)

var claudeWarmupUpstream = defaultClaudeProxyUpstream

// WarmupCandidate is an account whose windows a warm-up would start.
type WarmupCandidate struct {
	Service string
	Account string
	// Windows names the unstarted windows ("5h", "weekly", "fable weekly")
	// or holds WarmupReasonUnknownUsage.
	Windows []string
	addedAt time.Time
}

func (c WarmupCandidate) warmsFable() bool {
	return slices.Contains(c.Windows, warmupWindowFableWeekly)
}

// WarmupEvent is the outcome of one warm-up request.
type WarmupEvent struct {
	WarmupCandidate
	Err error
}

// PlanWarmups lists the accounts whose five-hour, weekly, or Fable weekly
// window has not started. It sends nothing.
func PlanWarmups(ctx context.Context, cfg Config) ([]WarmupCandidate, error) {
	lock, err := AcquireStateLock(ctx, cfg)
	if err != nil {
		return nil, err
	}
	state, err := LoadState(cfg.StatePath)
	lock.Release()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var candidates []WarmupCandidate
	for _, service := range cfg.Services {
		if service.Disabled || !service.UsesAccountHomes() || len(service.UsageCommand) > 0 {
			continue
		}
		serviceState := state.Service(service.Name)
		for _, name := range slices.Sorted(maps.Keys(serviceState.Accounts)) {
			account := serviceState.Accounts[name]
			windows := warmupWindows(service, account, now)
			if len(windows) == 0 {
				continue
			}
			candidates = append(candidates, WarmupCandidate{
				Service: service.Name,
				Account: name,
				Windows: windows,
				addedAt: account.AddedAt,
			})
		}
	}
	return candidates, nil
}

// WarmupOnce warms every planned account in turn. Accounts left over when
// ctx ends are picked up by the next call.
func WarmupOnce(ctx context.Context, cfg Config) ([]WarmupEvent, error) {
	candidates, err := PlanWarmups(ctx, cfg)
	if err != nil {
		return nil, err
	}
	events := make([]WarmupEvent, 0, len(candidates))
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			break
		}
		service, ok := cfg.Service(candidate.Service)
		if !ok {
			continue
		}
		var warmErr error
		switch {
		case isClaudeService(service):
			warmErr = warmClaudeAccount(ctx, cfg, service, candidate)
		case isCodexService(service):
			warmErr = warmCodexAccount(ctx, cfg, service, candidate.Account)
		default:
			continue
		}
		if ctx.Err() != nil && warmErr != nil {
			// Interrupted, not failed: leave no backoff behind.
			break
		}
		if warmErr != nil {
			warmErr = errors.New(sanitizeProbeError(warmErr))
		}
		// A request that went out must be recorded even if ctx just ended,
		// or the next cycle would warm the account again.
		recordCtx, cancelRecord := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		if err := recordWarmup(recordCtx, cfg, candidate, warmErr); err != nil && warmErr == nil {
			warmErr = err
		}
		cancelRecord()
		events = append(events, WarmupEvent{WarmupCandidate: candidate, Err: warmErr})
	}
	return events, nil
}

// warmupWindows reports what a warm-up would start for one account.
func warmupWindows(service ServiceConfig, account AccountState, now time.Time) []string {
	if account.CredentialsError != "" || now.Before(account.WarmupRetryAt) {
		return nil
	}
	var usage UsageSnapshot
	switch {
	case isClaudeService(service):
		if account.SetupTokenRevision == "" {
			return nil
		}
		known, ok := claudeProxyKnownUsage(account, account.SetupTokenRevision)
		if !ok {
			// Setup tokens cannot read the usage API, so an account the
			// proxy has never served has no other way to learn its windows.
			if warmedWithin(account.WarmupAt, fiveHourWindowLength, now) {
				return nil
			}
			return []string{WarmupReasonUnknownUsage}
		}
		usage = known
	case isCodexService(service):
		known, ok := codexProxyKnownUsage(account)
		if !ok {
			return nil
		}
		usage = known
	default:
		return nil
	}
	if windowExhausted(usage.FiveHour) || windowExhausted(usage.Weekly) {
		// Every model counts against these, so a request would only be
		// rejected; the window is running anyway.
		return nil
	}
	// Claude reports windows only on responses, which start them, so only
	// Codex can report a window whose reset floats ahead of the clock.
	floating := isCodexService(service)
	var windows []string
	for _, candidate := range []struct {
		name     string
		window   LimitWindow
		length   time.Duration
		warmupAt time.Time
	}{
		{warmupWindowFiveHour, usage.FiveHour, fiveHourWindowLength, account.WarmupAt},
		{warmupWindowWeekly, usage.Weekly, weeklyWindowLength, account.WarmupAt},
		// Only a Fable warm-up starts the Fable window; a Haiku one does not.
		{warmupWindowFableWeekly, usage.FableWeekly, weeklyWindowLength, account.FableWarmupAt},
	} {
		if candidate.window.Pct == nil || windowExhausted(candidate.window) ||
			warmedWithin(candidate.warmupAt, candidate.length, now) {
			continue
		}
		if windowUnstarted(candidate.window, usage.ObservedAt, candidate.length, floating, now) {
			windows = append(windows, candidate.name)
		}
	}
	return windows
}

func windowExhausted(window LimitWindow) bool {
	ratio, ok := window.Ratio()
	return ok && ratio >= 1
}

// warmedWithin reports that a warm-up started a window of this length that
// can still be running. Every request counts against every window, so one
// warm-up covers all of them.
func warmedWithin(warmupAt time.Time, length time.Duration, now time.Time) bool {
	return !warmupAt.IsZero() && now.Before(warmupAt.Add(length))
}

func windowUnstarted(window LimitWindow, observedAt time.Time, length time.Duration, floating bool, now time.Time) bool {
	switch {
	case window.ResetsAt.IsZero():
		// Providers omit the reset of a window that has not started.
		return true
	case !now.Before(window.ResetsAt):
		// The window reset and nothing has been observed since.
		return true
	case floating && window.Pct != nil && *window.Pct == 0 && !observedAt.IsZero():
		return window.ResetsAt.Sub(observedAt) >= length-warmupFloatTolerance
	}
	return false
}

// warmClaudeAccount sends a one-token message with the account's setup token
// and records the rate-limit headers like a proxied response. An idle Fable
// window needs a Fable model, which starts the other windows too.
func warmClaudeAccount(ctx context.Context, cfg Config, service ServiceConfig, candidate WarmupCandidate) error {
	accountName := candidate.Account
	token, status, err := LoadClaudeSetupTokenWithStatus(cfg, service.Name, accountName)
	if err != nil {
		return err
	}
	if !status.Usable {
		return errors.New("setup token unusable")
	}
	upstream := service.ProxyUpstream
	if upstream == "" {
		upstream = claudeWarmupUpstream
	}
	model := cmp.Or(service.WarmupModel, defaultClaudeWarmupModel)
	if candidate.warmsFable() {
		model = cmp.Or(service.WarmupFableModel, defaultClaudeFableWarmupModel)
	}
	body, err := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": 1,
		"system":     []map[string]string{{"type": "text", "text": claudeWarmupSystemPrompt}},
		"messages":   []map[string]string{{"role": "user", "content": warmupPrompt}},
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, claudeWarmupTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(upstream, "/")+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", claudeOAuthBetaHeader)
	req.Header.Set("User-Agent", "subswapper/1.0")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()

	observation := parseClaudeRateLimitHeaders(resp.Header, claudeProxyNow().UTC())
	unauthorized := resp.StatusCode == http.StatusUnauthorized
	route := claudeProxyRoute{Account: accountName, Revision: status.Revision}
	if err := recordClaudeProxyObservation(cfg, service, route, observation, unauthorized, false); err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("claude warm-up returned %s", resp.Status)
	}
	return nil
}

// warmCodexAccount runs one ephemeral Codex turn in the account home, which
// already holds the login and receives any token refresh.
func warmCodexAccount(ctx context.Context, cfg Config, service ServiceConfig, accountName string) error {
	home := AccountDir(cfg, service.Name, accountName)
	if _, _, ok := readCodexProxyCredentials(filepath.Join(home, "auth.json")); !ok {
		return errors.New("account home has no ChatGPT login")
	}
	workdir, err := os.MkdirTemp("", "subswapper-warmup-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(workdir) }()

	args := []string{
		"exec", "--ephemeral", "--ignore-user-config", "--ignore-rules", "--skip-git-repo-check",
		"--sandbox", "read-only", "--color", "never", "-C", workdir,
		"-c", `model_reasoning_effort="low"`,
	}
	if service.WarmupModel != "" {
		args = append(args, "-m", service.WarmupModel)
	}
	args = append(args, warmupPrompt)

	ctx, cancel := context.WithTimeout(ctx, codexWarmupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, codexCommand, args...)
	cmd.Env = envWithOverride(os.Environ(), "CODEX_HOME", home)
	cmd.Dir = workdir
	stderr := &tailBuffer{limit: codexWarmupStderrLimit}
	cmd.Stderr = stderr
	// Bound Wait after cancellation even if a grandchild keeps the pipes open.
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Run(); err != nil {
		if detail := lastLine(stderr.String()); detail != "" {
			return fmt.Errorf("codex warm-up failed: %w: %s", err, detail)
		}
		return fmt.Errorf("codex warm-up failed: %w", err)
	}
	return nil
}

// recordWarmup stores when the account was warmed, or when to try again.
func recordWarmup(ctx context.Context, cfg Config, candidate WarmupCandidate, warmErr error) error {
	lock, err := AcquireStateLock(ctx, cfg)
	if err != nil {
		return err
	}
	defer lock.Release()
	state, err := LoadState(cfg.StatePath)
	if err != nil {
		return err
	}
	serviceState := state.Service(candidate.Service)
	account, ok := serviceState.Accounts[candidate.Account]
	if !ok || !account.AddedAt.Equal(candidate.addedAt) {
		return errors.New("account changed during warm-up")
	}
	now := time.Now().UTC()
	if warmErr != nil {
		account.WarmupRetryAt = now.Add(warmupRetryBackoff)
	} else {
		account.WarmupAt = now
		account.WarmupRetryAt = time.Time{}
		if candidate.warmsFable() {
			account.FableWarmupAt = now
		}
	}
	serviceState.Accounts[candidate.Account] = account
	return SaveState(cfg.StatePath, state)
}

// tailBuffer keeps the last limit bytes written, where a failing CLI puts
// its error.
type tailBuffer struct {
	data  []byte
	limit int
}

func (b *tailBuffer) Write(data []byte) (int, error) {
	b.data = append(b.data, data...)
	if overflow := len(b.data) - b.limit; overflow > 0 {
		b.data = append(b.data[:0], b.data[overflow:]...)
	}
	return len(data), nil
}

func (b *tailBuffer) String() string { return string(b.data) }

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
