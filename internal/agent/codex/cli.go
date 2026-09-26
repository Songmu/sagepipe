package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/Songmu/sagepipe/internal/agent"
	"github.com/Songmu/sagepipe/internal/agent/cli"
)

// Runner invokes a new non-interactive Codex process for every request.
type Runner struct {
	options cli.Options
}

// New creates a Codex CLI runner. An empty Program selects "codex".
func New(options cli.Options) (*Runner, error) {
	options, err := options.Prepare("codex")
	if err != nil {
		return nil, err
	}
	return &Runner{options: options}, nil
}

func (r *Runner) Run(ctx context.Context, req agent.Request) (response agent.Response, err error) {
	if len(r.options.AllowedTools) != 0 {
		return response, errors.New("codex CLI: allowed-tools is unsupported")
	}
	args := append([]string(nil), r.options.Args...)
	args = append(args, "exec", "--json")
	if r.options.Model != "" {
		args = append(args, "--model", r.options.Model)
	}
	if len(req.NativeSchema) != 0 {
		var native bool
		native, err = cli.SafeNativeSchema(req.NativeSchema, true)
		if err != nil {
			return response, err
		}
		if native {
			var file *os.File
			file, err = os.CreateTemp("", "sagepipe-codex-schema-*.json")
			if err != nil {
				return response, errors.New("codex CLI: cannot create temporary output schema")
			}
			defer func() {
				if removeErr := os.Remove(file.Name()); removeErr != nil {
					err = errors.Join(err, errors.New("codex CLI: cannot remove temporary output schema"))
				}
			}()
			if _, err = file.Write(req.NativeSchema); err != nil {
				file.Close()
				return response, errors.New("codex CLI: cannot write temporary output schema")
			}
			if err = file.Close(); err != nil {
				return response, errors.New("codex CLI: cannot close temporary output schema")
			}
			args = append(args, "--output-schema", file.Name())
		} else {
			response.Warnings = append(response.Warnings, "Codex CLI native schema unsupported; using prompt instructions and local validation")
		}
	}
	args = append(args, "-")
	var final string
	var seenMessage, completed bool
	err = cli.Execute(ctx, "Codex", r.options, args, strings.NewReader(req.Prompt), req.OnLaunch, func(line []byte) error {
		if !utf8.Valid(line) {
			return errors.New("invalid Codex CLI JSON event")
		}
		var event struct {
			Type string `json:"type"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
			Usage *struct {
				InputTokens       int64 `json:"input_tokens"`
				OutputTokens      int64 `json:"output_tokens"`
				CachedInputTokens int64 `json:"cached_input_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(line, &event) != nil {
			return errors.New("invalid Codex CLI JSON event")
		}
		switch event.Type {
		case "item.completed":
			if event.Item.Type == "agent_message" {
				final, seenMessage = event.Item.Text, true
			}
		case "turn.completed":
			completed = true
			if event.Usage != nil {
				response.Usage = &agent.Usage{
					InputTokens: event.Usage.InputTokens, OutputTokens: event.Usage.OutputTokens,
					CachedInputTokens: event.Usage.CachedInputTokens,
				}
			}
		case "turn.failed", "error":
			return errors.New("codex CLI reported unsuccessful turn")
		}
		return nil
	})
	if err != nil {
		return agent.Response{}, err
	}
	if !seenMessage || !completed {
		return agent.Response{}, errors.New("codex CLI: missing final answer or completion event")
	}
	if err = cli.CheckResponseLimit(final, req.MaxResponseBytes); err != nil {
		return agent.Response{}, err
	}
	response.Text = final
	return response, nil
}

func (r *Runner) Close() error { return nil }

var _ agent.Runner = (*Runner)(nil)
