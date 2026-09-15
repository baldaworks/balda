//go:build windows

package runtimecatalog

import "os"

func openRootReadFile(root *os.Root, relative string) (*os.File, error) {
	return root.OpenFile(relative, os.O_RDONLY, 0)
}
