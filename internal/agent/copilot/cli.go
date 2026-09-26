package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/Songmu/sagepipe/internal/agent"
	"github.com/Songmu/sagepipe/internal/agent/cli"
)

// CLIRunner invokes a new non-interactive Copilot CLI process for every request.
type CLIRunner struct {
	options cli.Options
}

// NewCLI creates a Copilot CLI runner. An empty Program selects "copilot".
func NewCLI(options cli.Options) (*CLIRunner, error) {
	options, err := options.Prepare("copilot")
	if err != nil {
		return nil, err
	}
	return &CLIRunner{options: options}, nil
}

func (r *CLIRunner) Run(ctx context.Context, req agent.Request) (agent.Response, error) {
	var response agent.Response
	if r.options.Model != "" {
		response.Model = &agent.Model{ID: r.options.Model, Source: agent.ModelSourceExplicit}
	}
	if len(req.NativeSchema) != 0 {
		if _, err := cli.SafeNativeSchema(req.NativeSchema, false); err != nil {
			return response, err
		}
		response.Warnings = append(response.Warnings, "Copilot CLI does not support native output schemas; using prompt instructions and local validation")
	}
	args := append([]string(nil), r.options.Args...)
	args = append(args, "--output-format=json", "--no-ask-user")
	if r.options.Model != "" {
		args = append(args, "--model", r.options.Model)
	}
	if len(r.options.AllowedTools) != 0 {
		tools, err := cli.JoinAllowedTools(r.options.AllowedTools)
		if err != nil {
			return agent.Response{}, err
		}
		args = append(args, "--allow-tool="+tools)
	}
	var stdin io.Reader
	if len(req.Prompt) > 8<<10 {
		stdin = strings.NewReader(req.Prompt)
	} else {
		args = append(args, "-p", req.Prompt)
	}
	var final string
	var seenResult, seenMessage bool
	err := cli.Execute(ctx, "Copilot", r.options, args, stdin, req.OnLaunch, func(line []byte) error {
		if !utf8.Valid(line) {
			return errors.New("invalid Copilot CLI JSON event")
		}
		var event struct {
			Type     string `json:"type"`
			ExitCode *int   `json:"exitCode"`
			Data     struct {
				Content string `json:"content"`
				Phase   string `json:"phase"`
			} `json:"data"`
		}
		if json.Unmarshal(line, &event) != nil {
			return errors.New("invalid Copilot CLI JSON event")
		}
		switch event.Type {
		case "assistant.message":
			if event.Data.Phase == "final_answer" {
				final = event.Data.Content
				seenMessage = true
			}
		case "result":
			if event.ExitCode == nil || *event.ExitCode != 0 {
				return errors.New("copilot CLI reported unsuccessful result")
			}
			seenResult = true
		}
		return nil
	})
	if err != nil {
		return agent.Response{}, err
	}
	if seenResult && !seenMessage {
		return agent.Response{}, fmt.Errorf("copilot CLI: %w", agent.ErrNoTextResponse)
	}
	if !seenResult {
		return agent.Response{}, errors.New("copilot CLI: missing final answer or result event")
	}
	if err := cli.CheckResponseLimit(final, req.MaxResponseBytes); err != nil {
		return agent.Response{}, err
	}
	response.Text = final
	return response, nil
}

func (r *CLIRunner) Close() error { return nil }

var _ agent.Runner = (*CLIRunner)(nil)
