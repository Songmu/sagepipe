package sagepipe

import (
	"context"
	"encoding/json"
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
