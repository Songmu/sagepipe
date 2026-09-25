package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/Songmu/sagepipe"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stopRead := context.AfterFunc(ctx, func() {
		stop()
		os.Stdin.Close()
	})
	defer stopRead()

	err := sagepipe.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	if err != nil && err != flag.ErrHelp {
		exitCode := 2
		if ecoder, ok := err.(interface{ ExitCode() int }); ok {
			exitCode = ecoder.ExitCode()
		}
		return exitCode
	}
	return 0
}
