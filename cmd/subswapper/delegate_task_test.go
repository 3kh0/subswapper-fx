//go:build !windows

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDelegateTaskFile(t *testing.T) {
	binDir := t.TempDir()
	binary := filepath.Join(binDir, "subswapper")
	for _, build := range []struct{ output, source string }{
		{binary, "."},
		{filepath.Join(binDir, "codex"), "./testdata/delegate-provider"},
		{filepath.Join(binDir, "claude"), "./testdata/delegate-provider"},
	} {
		cmd := exec.Command("go", "build", "-buildvcs=false", "-o", build.output, build.source)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build: %v: %s", err, output)
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			dir := t.TempDir()
			config := writeHomeModeConfig(t, dir, provider)
			createHomeAccount(t, config, provider, "synthetic")
			if provider == "claude" {
				storeTestSetupToken(t, config, "synthetic", "synthetic-token-never-printed")
			}
			prompt := filepath.Join(dir, "prompt with spaces.txt")
			base := []string{"delegate", "-config", config, "-service", provider, "-cwd", dir, "-model", "synthetic", "-effort", "high", "-intent", "read-only", "-timeout", "10s"}
			for _, task := range []string{
				"Reply with OK and nothing else.\n",
				"  Review the supplied change.\nKeep 'quotes', $(literal), `literal`, and Unicode: λ.\n\n",
			} {
				if err := os.WriteFile(prompt, []byte(task), 0600); err != nil {
					t.Fatal(err)
				}
				var stdout, stderr bytes.Buffer
				cmd := exec.Command(binary, append(base, "-task-file", prompt)...)
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				if err := cmd.Run(); err != nil {
					t.Fatalf("task file: %v; stderr=%s", err, stderr.String())
				}
				var reply struct {
					Input string   `json:"input"`
					Args  []string `json:"args"`
				}
				if err := json.Unmarshal(stdout.Bytes(), &reply); err != nil {
					t.Fatalf("decode provider response: %v; %s", err, stdout.String())
				}
				if reply.Input != task {
					t.Errorf("task changed: got %q, want %q", reply.Input, task)
				}
				for _, arg := range reply.Args {
					if strings.Contains(arg, task) || strings.Contains(arg, prompt) {
						t.Errorf("prompt leaked into provider argument: %q", arg)
					}
				}
			}
			t.Run("provider-exit", func(t *testing.T) {
				t.Setenv("TASK_FIXTURE_EXIT", "23")
				output, err := exec.Command(binary, append(base, "-task-file", prompt)...).CombinedOutput()
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 23 {
					t.Fatalf("want exit 23, got %v; %s", err, output)
				}
			})
		})
	}
}

func TestDelegateTaskFileValidation(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "PRIVATE-task-file.txt")
	base := []string{"-service", "codex", "-cwd", dir, "-model", "synthetic", "-effort", "high", "-intent", "read-only"}
	for _, tc := range []struct {
		name, content, want string
		args                []string
	}{
		{"missing", "x", "exactly one", nil},
		{"both", "x", "exactly one", []string{"-task", "PRIVATE-task", "-task-file", file}},
		{"empty-text-and-file", "x", "exactly one", []string{"-task", "", "-task-file", file}},
		{"empty-file", " \n", "nonempty", []string{"-task-file", file}},
		{"nul", "PRIVATE\x00data", "nonempty", []string{"-task-file", file}},
		{"missing-file", "x", "unavailable", []string{"-task-file", filepath.Join(dir, "PRIVATE-missing")}},
		{"empty-path", "x", "unavailable", []string{"-task-file", ""}},
		{"directory", "x", "regular file", []string{"-task-file", dir}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(file, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			err := runDelegate(append(append([]string(nil), base...), tc.args...), &output, &output)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
			if strings.Contains(err.Error()+output.String(), "PRIVATE") || strings.Contains(err.Error()+output.String(), dir) {
				t.Fatalf("private task data leaked: %v %s", err, output.String())
			}
		})
	}
}

func TestDelegateTaskFileSizeLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(path, []byte("task"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, maxDelegateTaskFileBytes+1); err != nil {
		t.Fatal(err)
	}
	_, err := readDelegateTaskFile(path)
	if err == nil || !strings.Contains(err.Error(), "exceeds 16 MiB") {
		t.Fatalf("oversized prompt: %v", err)
	}
}

func TestDelegateTaskFileRejectsPipe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompt.pipe")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := readDelegateTaskFile(path)
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("pipe accepted as a task file: %v", err)
	}
	// Exercise the open used after pathname validation without a pipe writer.
	// This must also be safe if a regular file was replaced just before open.
	done := make(chan error, 1)
	go func() {
		file, err := openDelegateTaskFile(path)
		if err == nil {
			err = file.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		writer, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			_ = writer.Close()
		}
		t.Fatal("opening a replaced prompt file waited for a pipe writer")
	}
}
