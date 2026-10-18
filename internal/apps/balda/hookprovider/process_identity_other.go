//go:build !linux && !darwin

package hookprovider

import "github.com/baldaworks/balda/internal/apps/balda/hookcmd"

func nativePlatformSupported() bool { return false }
func readNativeProcess(int) (nativeProcessInfo, error) {
	return nativeProcessInfo{}, hookcmd.ErrUnsupported
}
func processHasArgument(int, string) (bool, error) { return false, hookcmd.ErrUnsupported }
func killNativeGroup(int) error                    { return hookcmd.ErrUnsupported }
