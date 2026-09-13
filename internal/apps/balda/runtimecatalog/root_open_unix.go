//go:build !windows

package runtimecatalog

import (
	"os"
	"syscall"
)

func openRootReadFile(root *os.Root, relative string) (*os.File, error) {
	return root.OpenFile(relative, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
