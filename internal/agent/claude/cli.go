package claude

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/Songmu/sagepipe/internal/agent"
	"github.com/Songmu/sagepipe/internal/agent/cli"
)

// Runner invokes a new Claude CLI process for every request.
type Runner struct {
	options cli.Options
}

// New creates a Claude CLI runner. An empty Program selects "claude".
func New(options cli.Options) (*Runner, error) {
	options, err := options.Prepare("claude")
	if err != nil {
		return nil, err
	}
	return &Runner{options: options}, nil
}

func (r *Runner) Run(ctx context.Context, req agent.Request) (agent.Response, error) {
	var response agent.Response
	args := append([]string(nil), r.options.Args...)
	args = append(args, "-p", "--output-format=json")
	if r.options.Model != "" {
		args = append(args, "--model", r.options.Model)
	}
	if len(r.options.AllowedTools) != 0 {
		for _, tool := range r.options.AllowedTools {
			if strings.TrimSpace(tool) == "" || strings.HasPrefix(tool, "-") {
				return response, errors.New("claude CLI: invalid allowed tool rule")
			}
		}
		args = append(args, "--allowedTools")
		args = append(args, r.options.AllowedTools...)
	}
	native := false
	if len(req.NativeSchema) != 0 {
		var err error
		native, err = cli.SafeNativeSchema(req.NativeSchema, false)
		if err != nil {
			return response, err
		}
		if native && len(req.NativeSchema) <= 8<<10 {
			args = append(args, "--json-schema", string(req.NativeSchema))
		} else {
			native = false
			response.Warnings = append(response.Warnings, "Claude CLI native schema unsupported or too large for argv; using prompt instructions and local validation")
		}
	}
	if len(req.Prompt) > 10_000_000 {
		return agent.Response{}, errors.New("claude CLI: prompt exceeds the 10 MB stdin limit")
	}
	var seen bool
	var final string
	err := cli.Execute(ctx, "Claude", r.options, args, strings.NewReader(req.Prompt), req.OnLaunch, func(line []byte) error {
		if !utf8.Valid(line) {
			return errors.New("invalid Claude CLI JSON result")
		}
		var result struct {
			Type             string          `json:"type"`
			Subtype          string          `json:"subtype"`
			IsError          bool            `json:"is_error"`
			Result           *string         `json:"result"`
			StructuredOutput json.RawMessage `json:"structured_output"`
			Usage            *struct {
				InputTokens       int64 `json:"input_tokens"`
				OutputTokens      int64 `json:"output_tokens"`
				CachedInputTokens int64 `json:"cache_read_input_tokens"`
			} `json:"usage"`
		}
		if seen || json.Unmarshal(line, &result) != nil || result.Type != "result" {
			return errors.New("invalid Claude CLI JSON result")
		}
		if result.IsError || result.Subtype != "success" {
			return errors.New("claude CLI reported unsuccessful result")
		}
		if native {
			if len(result.StructuredOutput) == 0 || string(result.StructuredOutput) == "null" {
				return errors.New("claude CLI: missing structured output")
			}
			final = string(result.StructuredOutput)
		} else {
			if result.Result == nil {
				return errors.New("claude CLI: missing final answer")
			}
			final = *result.Result
		}
		if result.Usage != nil {
			response.Usage = &agent.Usage{
				InputTokens: result.Usage.InputTokens, OutputTokens: result.Usage.OutputTokens,
				CachedInputTokens: result.Usage.CachedInputTokens,
			}
		}
		seen = true
		return nil
	})
	if err != nil {
		return agent.Response{}, err
	}
	if !seen {
		return agent.Response{}, errors.New("claude CLI: missing result")
	}
	if err := cli.CheckResponseLimit(final, req.MaxResponseBytes); err != nil {
		return agent.Response{}, err
	}
	response.Text = final
	return response, nil
}

func (r *Runner) Close() error { return nil }

var _ agent.Runner = (*Runner)(nil)
