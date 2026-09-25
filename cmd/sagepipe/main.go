package main

import (
	"context"
	"flag"
	"os"

	"github.com/Songmu/sagepipe"
)

func main() {
	err := sagepipe.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)
	if err != nil && err != flag.ErrHelp {
		exitCode := 2
		if ecoder, ok := err.(interface{ ExitCode() int }); ok {
			exitCode = ecoder.ExitCode()
		}
		os.Exit(exitCode)
	}
}
