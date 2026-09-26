package sagepipe

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Songmu/sagepipe/internal/agent/factory"
	"github.com/Songmu/sagepipe/internal/config"
	"github.com/Songmu/sagepipe/internal/pipeline"
)

const cmdName = "sagepipe"

type exitError int

func (e exitError) Error() string { return fmt.Sprintf("%s exited with status %d", cmdName, e) }
func (e exitError) ExitCode() int { return int(e) }

// Run transforms records from standard input and writes structured diagnostics
// to errStream. A non-nil error has already been reported to errStream.
func Run(ctx context.Context, argv []string, outStream, errStream io.Writer) error {
	if len(argv) == 1 && (argv[0] == "-version" || argv[0] == "--version") {
		if err := printVersion(outStream); err != nil {
			pipeline.ReportError(errStream, "output_write_failed", "output", "Could not write version")
			return exitError(2)
		}
		return nil
	}
	startupCWD, err := os.Getwd()
	if err != nil {
		pipeline.ReportError(errStream, "working_directory_failed", "startup", "Could not get current directory")
		return exitError(2)
	}
	cfg, err := config.Parse(argv, startupCWD)
	if errors.Is(err, flag.ErrHelp) {
		if _, err := io.WriteString(outStream, config.Usage()); err != nil {
			pipeline.ReportError(errStream, "output_write_failed", "output", "Could not write usage")
			return exitError(2)
		}
		return nil
	}
	if err != nil {
		pipeline.ReportError(errStream, "invalid_config", "config", err.Error())
		return exitError(2)
	}
	if err := os.Chdir(cfg.CWD); err != nil {
		pipeline.ReportError(errStream, "working_directory_failed", "startup", "Could not change working directory")
		return exitError(2)
	}
	runner, err := factory.New(cfg)
	if err != nil {
		pipeline.ReportError(errStream, "agent_start_failed", "agent", err.Error())
		return exitError(2)
	}
	if code := pipeline.Run(ctx, cfg, os.Stdin, outStream, errStream, runner); code != 0 {
		return exitError(code)
	}
	return nil
}

func printVersion(out io.Writer) error {
	_, err := fmt.Fprintf(out, "%s v%s (rev:%s)\n", cmdName, version, revision)
	return err
}
