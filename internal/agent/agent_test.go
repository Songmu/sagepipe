package agent

import (
	"reflect"
	"testing"
)

func TestNewLaunchRedactsSensitiveArguments(t *testing.T) {
	input := []string{
		"--disable-mcp-server=workiq",
		"-p", "private prompt",
		"--json-schema=private-schema",
		"--allowedTools", "private-tool", "other-tool",
		"--allow-tool=private-policy",
		"--api_key=private-api-key",
		"--api-version", "private-api-version",
		"--key-file=private-key-file",
		"--authentication-mode", "private-auth-mode",
		"--access_token", "private-token",
		"--model", "test-model",
	}
	launch := NewLaunch("copilot", input, "/tmp/project")
	want := []string{
		"--disable-mcp-server=workiq",
		"-p", "<redacted>",
		"--json-schema=<redacted>",
		"--allowedTools", "<redacted>", "<redacted>",
		"--allow-tool=<redacted>",
		"--api_key=<redacted>",
		"--api-version", "<redacted>",
		"--key-file=<redacted>",
		"--authentication-mode", "<redacted>",
		"--access_token", "<redacted>",
		"--model", "test-model",
	}
	if launch.Command() != "copilot" || launch.CWD() != "/tmp/project" ||
		!reflect.DeepEqual(launch.Args(), want) {
		t.Fatalf("launch = %q %q %q, want %q %q %q",
			launch.Command(), launch.Args(), launch.CWD(), "copilot", want, "/tmp/project")
	}
	if !reflect.DeepEqual(input, []string{
		"--disable-mcp-server=workiq",
		"-p", "private prompt",
		"--json-schema=private-schema",
		"--allowedTools", "private-tool", "other-tool",
		"--allow-tool=private-policy",
		"--api_key=private-api-key",
		"--api-version", "private-api-version",
		"--key-file=private-key-file",
		"--authentication-mode", "private-auth-mode",
		"--access_token", "private-token",
		"--model", "test-model",
	}) {
		t.Fatalf("NewLaunch mutated input: %q", input)
	}
	args := launch.Args()
	args[0] = "mutated"
	if launch.Args()[0] != "--disable-mcp-server=workiq" {
		t.Fatal("Launch.Args returned mutable internal state")
	}
}
