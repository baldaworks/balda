package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/baldaworks/balda/internal/apps/balda/hooknative/claude"
	"github.com/baldaworks/balda/internal/apps/balda/hooknative/codex"
	"github.com/baldaworks/balda/internal/apps/balda/hooknative/opencode"
	"github.com/baldaworks/balda/internal/apps/balda/hooknative/proxyruntime"
	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"
	"github.com/baldaworks/balda/internal/apps/balda/shutdown"
)

func main() {
	if handled, code := proxyruntime.RunCommandMode(os.Args[1:], claude.Protocol{}, codex.Protocol{}, opencode.Protocol{}); handled {
		os.Exit(code)
	}
	if handled, code := mcpfx.RunStdioMode(os.Args[1:]); handled {
		os.Exit(code)
	}
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
