package factory

import (
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strings"
	"unicode"

	"github.com/Songmu/sagepipe/internal/agent"
	"github.com/Songmu/sagepipe/internal/agent/acp"
	"github.com/Songmu/sagepipe/internal/agent/claude"
	"github.com/Songmu/sagepipe/internal/agent/cli"
	"github.com/Songmu/sagepipe/internal/agent/codex"
	"github.com/Songmu/sagepipe/internal/agent/copilot"
	"github.com/Songmu/sagepipe/internal/config"
)

// New selects an adapter without starting a subprocess.
func New(cfg config.Config) (agent.Runner, error) {
	selected := cfg.Agent
	switch selected.Protocol {
	case "acp":
		return newACP(selected, cfg.AllowedTools)
	case "cli":
		return newCLI(selected, cfg.AllowedTools, cfg.MaxResponseBytes)
	default:
		return nil, fmt.Errorf("unsupported agent protocol %q", selected.Protocol)
	}
}

func newACP(selected config.AgentConfig, allowedTools string) (agent.Runner, error) {
	switch selected.Provider {
	case "copilot":
		if err := requireProgram("copilot"); err != nil {
			return nil, err
		}
		return copilot.NewACP(selected.Model, selected.CWD, allowedTools, selected.Args)
	case "":
		if allowedTools != "" {
			return nil, errors.New("custom ACP agents do not support allowed-tools")
		}
		if err := requireProgram(selected.Command); err != nil {
			return nil, err
		}
		return acp.New(acp.Options{
			Command: selected.Command, Args: selected.Args, CWD: selected.CWD, Model: selected.Model,
		})
	default:
		return nil, fmt.Errorf("agent %q does not support ACP", selected.Provider)
	}
}

func newCLI(selected config.AgentConfig, allowedTools string, maxResponseBytes int64) (agent.Runner, error) {
	tools, err := splitAllowedTools(allowedTools)
	if err != nil {
		return nil, err
	}
	program := selected.Provider
	if program != "copilot" && program != "claude" && program != "codex" {
		return nil, fmt.Errorf("agent %q does not support CLI", program)
	}
	switch program {
	case "copilot":
		if len(tools) != 0 {
			if _, err := cli.JoinAllowedTools(tools); err != nil {
				return nil, err
			}
		}
	case "claude":
		for _, tool := range tools {
			if strings.HasPrefix(tool, "-") {
				return nil, errors.New("invalid Claude allowed-tools rule")
			}
		}
	case "codex":
		if len(tools) != 0 {
			return nil, errors.New("codex CLI does not support allowed-tools")
		}
	}
	if err := requireProgram(program); err != nil {
		return nil, err
	}
	eventBytes, err := eventLimit(maxResponseBytes)
	if err != nil {
		return nil, err
	}
	options := cli.Options{
		Program: program, Args: selected.Args, Dir: selected.CWD, Model: selected.Model,
		AllowedTools: tools, MaxOutputBytes: eventBytes,
	}
	switch program {
	case "copilot":
		return copilot.NewCLI(options)
	case "claude":
		return claude.New(options)
	default:
		return codex.New(options)
	}
}

func requireProgram(program string) error {
	if program == "" {
		return errors.New("agent executable is required")
	}
	if _, err := exec.LookPath(program); err != nil {
		return fmt.Errorf("agent executable %q is not available", program)
	}
	return nil
}

// splitAllowedTools keeps spaces inside a tool's argument pattern.
func splitAllowedTools(value string) ([]string, error) {
	var tools []string
	var current strings.Builder
	depth := 0
	flush := func() {
		if current.Len() > 0 {
			tools = append(tools, current.String())
			current.Reset()
		}
	}
	for _, r := range value {
		switch {
		case unicode.IsSpace(r) && depth == 0:
			flush()
		case r == '(':
			depth++
			current.WriteRune(r)
		case r == ')':
			depth--
			if depth < 0 {
				return nil, errors.New("unbalanced allowed-tools pattern")
			}
			current.WriteRune(r)
		default:
			current.WriteRune(r)
		}
	}
	if depth != 0 {
		return nil, errors.New("unbalanced allowed-tools pattern")
	}
	flush()
	return tools, nil
}

func eventLimit(responseBytes int64) (int64, error) {
	if responseBytes <= 0 || responseBytes > (math.MaxInt64-(1<<20))/6 {
		return 0, errors.New("max_response_bytes is too large for bounded CLI output")
	}
	limit := responseBytes*6 + (1 << 20)
	if limit > int64(int(^uint(0)>>1))-1 {
		return 0, errors.New("max_response_bytes exceeds platform CLI output limit")
	}
	return limit, nil
}
