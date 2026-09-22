package main

import (
	"fmt"
	"os"
)

func main() {
	if err := execute(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
