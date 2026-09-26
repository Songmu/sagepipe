package sagepipe

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var out, diag strings.Builder
	if err := Run(context.Background(), []string{"-version"}, &out, &diag); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "sagepipe v") || diag.Len() != 0 {
		t.Errorf("stdout=%q stderr=%q", out.String(), diag.String())
	}
}

func TestHelp(t *testing.T) {
	var out, diag strings.Builder
	if err := Run(context.Background(), []string{"--help"}, &out, &diag); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Usage: sagepipe") || diag.Len() != 0 {
		t.Errorf("stdout=%q stderr=%q", out.String(), diag.String())
	}
}

func TestInvalidConfigurationUsesJSONDiagnostics(t *testing.T) {
	var out, diag strings.Builder
	err := Run(context.Background(), []string{"--mode", "unknown"}, &out, &diag)
	if err == nil {
		t.Fatal("invalid mode succeeded")
	}
	if e, ok := err.(interface{ ExitCode() int }); !ok || e.ExitCode() != 2 {
		t.Errorf("exit error = %v, want code 2", err)
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(diag.String())), &event); err != nil {
		t.Fatalf("invalid JSON diagnostic: %s: %v", diag.String(), err)
	}
	if event["code"] != "invalid_config" || event["stage"] != "config" || event["message"] == nil || out.Len() != 0 {
		t.Errorf("stdout=%q diagnostic=%v", out.String(), event)
	}
}

func TestRunChangesWorkingDirectory(t *testing.T) {
	startupCWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(startupCWD); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	}()

	target := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	configPath := target + string(os.PathSeparator) + "config.md"
	configData := fmt.Sprintf("---\nagent:\n  protocol: acp\n  command: %q\nmode: map\n---\n", executable)
	if err := os.WriteFile(configPath, []byte(configData), 0600); err != nil {
		t.Fatal(err)
	}
	var out, diag strings.Builder
	err = Run(context.Background(), []string{
		"--cwd", target,
		"--config", "config.md",
	}, &out, &diag)
	if err != nil {
		t.Fatalf("Run failed: %v; diagnostics: %s", err, diag.String())
	}
	got, getwdErr := os.Getwd()
	if getwdErr != nil {
		t.Fatal(getwdErr)
	}
	gotInfo, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(gotInfo, targetInfo) {
		t.Fatalf("working directory = %q, want %q", got, target)
	}
}
