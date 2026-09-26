package pipeline

import (
	"context"
	"io"
	"log/slog"
)

type diagnostics struct {
	logger *slog.Logger
}

func newDiagnostics(w io.Writer, verbose bool) *diagnostics {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
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
	newDiagnostics(w, false).log(slog.LevelError, code, stage, message, 0)
}
