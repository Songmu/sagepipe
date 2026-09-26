package agent

import (
	"reflect"
	"testing"
)

func TestNewLaunchCopiesArguments(t *testing.T) {
	input := []string{
		"--disable-mcp-server=workiq",
		"-p", "private prompt",
		"--additional-mcp-config", `{"env":{"API_TOKEN":"private-mcp-token"}}`,
		"--acp",
		"--model", "test-model",
	}
	launch := NewLaunch("copilot", input, "/tmp/project")
	want := []string{
		"--disable-mcp-server=workiq",
		"-p", "private prompt",
		"--additional-mcp-config", `{"env":{"API_TOKEN":"private-mcp-token"}}`,
		"--acp",
		"--model", "test-model",
	}
	if launch.Command() != "copilot" || launch.CWD() != "/tmp/project" ||
		!reflect.DeepEqual(launch.Args(), want) {
		t.Fatalf("launch = %q %q %q, want %q %q %q",
			launch.Command(), launch.Args(), launch.CWD(), "copilot", want, "/tmp/project")
	}
	if !reflect.DeepEqual(input, want) {
		t.Fatalf("NewLaunch mutated input: %q", input)
	}
	args := launch.Args()
	args[0] = "mutated"
	if launch.Args()[0] != "--disable-mcp-server=workiq" {
		t.Fatal("Launch.Args returned mutable internal state")
	}
}
