package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/normahq/balda/internal/apps/balda/shutdown"
)

func main() {
	os.Exit(run())
}

func run() int {
	if err := executeFn(); err != nil {
		if shutdown.IsExpected(err) {
			return 0
		}

		var unprintedErr *unprintedCLIError
		if errors.As(err, &unprintedErr) {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		}
		var exitCoder interface{ ExitCode() int }
		if errors.As(err, &exitCoder) {
			return exitCoder.ExitCode()
		}
		return 1
	}

	return 0
}
