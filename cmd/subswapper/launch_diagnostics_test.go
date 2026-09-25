//go:build !windows

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/3kh0/subswapper-fx/internal/subswapper"
)

func TestClaudeLaunchDiagnostics(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "subswapper")
	if output, err := exec.Command("go", "build", "-buildvcs=false", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build launcher: %v: %s", err, output)
	}
	providerDir := t.TempDir()
	provider := filepath.Join(providerDir, "claude")
	if output, err := exec.Command("go", "build", "-buildvcs=false", "-o", provider, "./testdata/launch-provider").CombinedOutput(); err != nil {
		t.Fatalf("build provider fixture: %v: %s", err, output)
	}
	for _, test := range []struct {
		name, want string
	}{
		{"missing", "Claude setup token is missing"},
		{"expired", "Claude setup token has expired"},
		{"revision", "Claude setup token metadata does not match"},
		{"corrupt-token", "Claude setup token storage is invalid"},
		{"unsafe-storage", "Claude setup token storage is invalid"},
		{"corrupt-state", "launcher state is invalid"},
		{"read-only", "launcher filesystem is read-only"},
		{"permission", "launcher filesystem access was denied"},
		{"runtime-permission", "launcher filesystem access was denied"},
		{"executable-missing", "Claude executable could not be started"},
		{"executable-permission", "Claude executable could not be started"},
		{"auth-failed", "Claude authentication check failed"},
		{"auth-rejected", "Claude authentication is not usable"},
		{"auth-malformed", "Claude authentication is not usable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			config := writeHomeModeConfig(t, dir, "claude")
			const account = "SYNTHETIC-PRIVATE-ACCOUNT"
			const token = "SYNTHETIC-PRIVATE-TOKEN"
			createHomeAccount(t, config, "claude", account)
			cfg, err := subswapper.LoadConfig(config)
			if err != nil {
				t.Fatal(err)
			}
			if test.name != "missing" {
				storeTestSetupToken(t, config, account, token)
			}
			tokenPath := filepath.Join(dir, "tokens", "claude", account, "setup-token.json")
			switch test.name {
			case "expired", "revision":
				data, err := os.ReadFile(tokenPath)
				if err != nil {
					t.Fatal(err)
				}
				var envelope map[string]any
				if err := json.Unmarshal(data, &envelope); err != nil {
					t.Fatal(err)
				}
				if test.name == "expired" {
					envelope["stored_at"] = time.Now().Add(-48 * time.Hour)
					envelope["expires_at"] = time.Now().Add(-24 * time.Hour)
				} else {
					envelope["revision"] = "SYNTHETIC-PRIVATE-REVISION"
				}
				data, err = json.Marshal(envelope)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(tokenPath, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "corrupt-token":
				if err := os.WriteFile(tokenPath, []byte(`{"token":"`+token+`", broken`), 0600); err != nil {
					t.Fatal(err)
				}
			case "unsafe-storage":
				if err := os.Chmod(tokenPath, 0644); err != nil {
					t.Fatal(err)
				}
			case "corrupt-state":
				if err := os.WriteFile(cfg.StatePath, []byte(`{"private":"`+account+`", broken`), 0600); err != nil {
					t.Fatal(err)
				}
			case "read-only":
				if runtime.GOOS != "linux" {
					t.Skip("requires Linux read-only sysfs")
				}
				lockPath := cfg.StatePath + ".lock"
				if err := os.Remove(lockPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/sys/subswapper-diagnostic-test.lock", lockPath); err != nil {
					t.Fatal(err)
				}
				file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
				if file != nil {
					_ = file.Close()
					t.Fatal("sysfs unexpectedly writable")
				}
				if !errors.Is(err, syscall.EROFS) {
					t.Skipf("sysfs does not produce EROFS: %v", err)
				}
			case "permission", "runtime-permission":
				if os.Geteuid() == 0 {
					t.Skip("requires unprivileged permission enforcement")
				}
				path := cfg.StatePath + ".lock"
				if test.name == "runtime-permission" {
					path = filepath.Dir(subswapper.RuntimeHome(*cfg, cfg.Services[0], account))
				}
				if err := os.Chmod(path, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(path, 0700) })
			}
			binDir := providerDir
			if strings.HasPrefix(test.name, "executable-") {
				binDir = t.TempDir()
				if test.name == "executable-permission" {
					if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte("not executable"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			t.Setenv("PATH", binDir)
			t.Setenv("DIAGNOSTIC_TEST_AUTH", test.name)
			for _, boundary := range []string{"home", "delegate"} {
				t.Run(boundary, func(t *testing.T) {
					args := []string{"home", "run", "-config", config, "-service", "claude", "-account", account}
					if boundary == "delegate" {
						args = []string{"delegate", "-config", config, "-service", "claude", "-account", account, "-cwd", dir, "-model", "synthetic", "-effort", "high", "-intent", "read-only", "-task", "SYNTHETIC-PRIVATE-TASK"}
					}
					cmd := exec.Command(binary, args...)
					var stdout, stderr bytes.Buffer
					cmd.Stdout, cmd.Stderr = &stdout, &stderr
					if err := cmd.Run(); err == nil {
						t.Fatal("launch unexpectedly succeeded")
					}
					if !strings.Contains(stderr.String(), test.want) {
						t.Errorf("want %q in stderr, got %q", test.want, stderr.String())
					}
					combined := stdout.String() + stderr.String()
					for _, secret := range []string{"SYNTHETIC-PRIVATE", dir, providerDir} {
						if strings.Contains(combined, secret) {
							t.Errorf("output leaked private detail: %q", combined)
						}
					}
					if stdout.Len() != 0 {
						t.Errorf("provider task executed: %q", stdout.String())
					}
				})
			}
		})
	}
}

func TestClaudeReadOnlyDiagnostic(t *testing.T) {
	// Some hosts return EACCES for sysfs before the filesystem can return
	// EROFS. Keep the wrapped-errno contract covered on those hosts too.
	err := &os.PathError{Op: "open", Path: "SYNTHETIC-PRIVATE/state.lock", Err: syscall.EROFS}
	diagnostic := diagnoseClaudeTokenFailure(err)
	if !strings.Contains(diagnostic.Error(), "launcher filesystem is read-only") {
		t.Fatalf("read-only storage misdiagnosed: %v", diagnostic)
	}
	if strings.Contains(diagnostic.Error(), "SYNTHETIC-PRIVATE") {
		t.Fatalf("private path leaked: %v", diagnostic)
	}
}
