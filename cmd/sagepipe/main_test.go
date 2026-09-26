//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSignalCancelsBlockedInput(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "sagepipe")
	build := exec.Command("go", "build", "-o", bin, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build sagepipe: %v\n%s", err, output)
	}
	cfg := filepath.Join(t.TempDir(), "config.md")
	content := fmt.Sprintf("---\nagent:\n  protocol: acp\n  command: %q\nmode: map\n---\n", bin)
	if err := os.WriteFile(cfg, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		signal os.Signal
	}{
		{"interrupt", os.Interrupt},
		{"termination", syscall.SIGTERM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diagPath := filepath.Join(t.TempDir(), "diagnostics")
			diag, err := os.OpenFile(diagPath, os.O_CREATE|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer diag.Close()
			cmd := exec.Command(bin, "-v", "--config", cfg)
			cmd.Stderr = diag
			var output strings.Builder
			cmd.Stdout = &output
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				data, err := os.ReadFile(diagPath)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(data), `"code":"agent_selected"`) {
					break
				}
				if time.Now().After(deadline) {
					cmd.Process.Kill()
					cmd.Wait()
					t.Fatalf("pipeline did not start: %s", data)
				}
				time.Sleep(10 * time.Millisecond)
			}
			time.Sleep(50 * time.Millisecond)
			if err := cmd.Process.Signal(tc.signal); err != nil {
				cmd.Process.Kill()
				cmd.Wait()
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				exit, ok := errors.AsType[*exec.ExitError](err)
				if !ok || exit.ExitCode() != 2 {
					t.Fatalf("signal exit = %v, want status 2", err)
				}
			case <-time.After(3 * time.Second):
				cmd.Process.Kill()
				<-done
				t.Fatal("pipeline did not stop after signal")
			}
			data, err := os.ReadFile(diagPath)
			if err != nil {
				t.Fatal(err)
			}
			if output.Len() != 0 || strings.Count(string(data), `"code":"input_cancelled"`) != 1 ||
				strings.Contains(string(data), `"code":"input_read_failed"`) ||
				!strings.Contains(string(data), `"exit_code":2`) {
				t.Errorf("stdout=%q diagnostics=%s", output.String(), data)
			}
		})
	}

	t.Run("active agent", func(t *testing.T) {
		agentDir := t.TempDir()
		marker := filepath.Join(agentDir, "started")
		script := "#!/bin/sh\nprintf '%s' \"$$\" > \"$SAGEPIPE_AGENT_MARKER\"\nexec sleep 5\n"
		if err := os.WriteFile(filepath.Join(agentDir, "copilot"), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bin, "-v", "--agent", "copilot", "--protocol", "cli", "--mode", "map", "--prompt", "test")
		cmd.Env = append(os.Environ(), "PATH="+agentDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"SAGEPIPE_AGENT_MARKER="+marker)
		cmd.Stdin = strings.NewReader("input\n")
		var output, diag strings.Builder
		cmd.Stdout, cmd.Stderr = &output, &diag
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			if _, err := os.Stat(marker); err == nil {
				break
			}
			if time.Now().After(deadline) {
				cmd.Process.Kill()
				cmd.Wait()
				t.Fatal("mock agent did not start: " + diag.String())
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			cmd.Process.Kill()
			cmd.Wait()
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			exit, ok := errors.AsType[*exec.ExitError](err)
			if !ok || exit.ExitCode() != 2 {
				t.Fatalf("signal exit = %v, want status 2", err)
			}
		case <-time.After(3 * time.Second):
			cmd.Process.Kill()
			<-done
			t.Fatal("active agent did not stop after interrupt")
		}
		if output.Len() != 0 || strings.Count(diag.String(), `"code":"agent_cancelled"`) != 1 ||
			strings.Contains(diag.String(), `"code":"input_read_failed"`) ||
			!strings.Contains(diag.String(), `"exit_code":2`) {
			t.Errorf("stdout=%q diagnostics=%s", output.String(), diag.String())
		}
	})
}
