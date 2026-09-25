package copilot

import (
	"errors"
	"strings"

	"github.com/Songmu/sagepipe/internal/agent"
	"github.com/Songmu/sagepipe/internal/agent/acp"
)

// NewACP configures Copilot in stdio ACP mode; the first Run starts it.
// Tool availability is process-wide across the runner's sessions.
func NewACP(model, cwd, allowedTools string) (agent.Runner, error) {
	args := []string{"--acp", "--stdio"}
	if model != "" {
		args = append(args, "--model", model)
	}
	if tools := strings.Fields(allowedTools); allowedTools != "" {
		if len(tools) == 0 {
			return nil, errors.New("copilot allowed-tools must contain tool names")
		}
		for _, tool := range tools {
			if strings.Contains(tool, ",") {
				return nil, errors.New("copilot allowed-tools must be space-separated")
			}
			switch strings.ToLower(tool) {
			case "read", "write", "shell", "url", "memory":
				return nil, errors.New("copilot allowed-tools requires tool names (for example view), not permission kinds")
			}
		}
		args = append(args, "--available-tools="+strings.Join(tools, ","))
		return acp.New(acp.Options{
			Command: "copilot", Args: args, CWD: cwd, FilterAgentChunk: filterToolNotice,
		})
	}
	return acp.New(acp.Options{Command: "copilot", Args: args, CWD: cwd})
}

func filterToolNotice(text string) (bool, error) {
	if strings.Contains(text, "Info: Unknown tool name in the tool allowlist:") {
		return false, errors.New("copilot rejected a name in allowed-tools")
	}
	names, ok := strings.CutPrefix(text, "Info: Disabled tools: ")
	if !ok {
		return false, nil
	}
	for _, name := range strings.Split(names, ", ") {
		if name == "" || strings.ContainsAny(name, " ,\t\r\n{}[]\"") {
			return false, errors.New("copilot sent an unrecognized tool notice")
		}
	}
	return true, nil
}
