package pipeline

import (
	"context"
	"io"
	"log/slog"
	"strings"
)

type diagnostics struct {
	logger *slog.Logger
}

func newDiagnostics(w io.Writer) *diagnostics {
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.MessageKey {
				a.Key = "message"
			}
			return a
		},
	})
	return &diagnostics{logger: slog.New(handler)}
}

func (d *diagnostics) log(level slog.Level, code, stage, message string, line int, extra ...any) {
	args := []any{"code", code, "stage", stage}
	if line > 0 {
		args = append(args, "line", line)
	}
	args = append(args, extra...)
	d.logger.Log(context.Background(), level, message, args...)
}

// ReportError emits a configuration or startup error before the pipeline starts.
func ReportError(w io.Writer, code, stage, message string) {
	newDiagnostics(w).log(slog.LevelError, code, stage, message, 0)
}

func safeLaunchArgs(args []string) []string {
	safe := append([]string(nil), args...)
	for i := 0; i < len(safe); i++ {
		arg := safe[i]
		name, _, inline := strings.Cut(arg, "=")
		if launchArgList(name) {
			if inline {
				safe[i] = name + "=<redacted>"
				continue
			}
			for i+1 < len(safe) && !strings.HasPrefix(safe[i+1], "-") {
				i++
				safe[i] = "<redacted>"
			}
			continue
		}
		if sensitiveLaunchArg(name) {
			if inline {
				safe[i] = name + "=<redacted>"
			} else if i+1 < len(safe) {
				safe[i+1] = "<redacted>"
				i++
			}
			continue
		}
	}
	return safe
}

func launchArgList(name string) bool {
	return name == "--allowedTools" || name == "--allow-tool" || name == "--available-tools"
}

func sensitiveLaunchArg(name string) bool {
	switch name {
	case "-p", "--prompt", "--json-schema":
		return true
	}
	name = strings.ToLower(name)
	return strings.Contains(name, "token") ||
		strings.Contains(name, "secret") ||
		strings.Contains(name, "password") ||
		strings.Contains(name, "credential") ||
		strings.Contains(name, "authorization") ||
		strings.Contains(name, "api-key") ||
		strings.Contains(name, "apikey")
}
