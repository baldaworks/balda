//go:build !unix

package httpfx

import (
	"fmt"
	"net"
)

func listenUnix(string) (net.Listener, error) {
	return nil, fmt.Errorf("private Unix listeners are not supported on this platform")
}
