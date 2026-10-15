package httpfx

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// GatewayCallbackProvider prepares one transport's enabled callbacks at startup.
type GatewayCallbackProvider func(context.Context) ([]GatewayCallback, error)

// GatewayCallback is one transport-owned HTTP endpoint offered to the shared listener.
type GatewayCallback struct {
	Owner        string
	Transport    string
	Endpoint     string
	Handler      http.Handler
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

// AddGatewayCallbacks registers canonical gateway paths.
func (r *Registry) AddGatewayCallbacks(callbacks []GatewayCallback) error {
	for _, callback := range callbacks {
		if !validGatewaySegment(callback.Transport) || !validGatewaySegment(callback.Endpoint) {
			return fmt.Errorf("invalid gateway callback for %q", callback.Owner)
		}
		canonical := r.gatewayPath + "/" + callback.Transport + "/" + callback.Endpoint
		handler := callbackDeadlineHandler(callback.Handler, callback.ReadTimeout, callback.WriteTimeout)
		if err := r.AddGateway(callback.Owner, canonical, handler); err != nil {
			return err
		}
	}
	return nil
}

func callbackDeadlineHandler(handler http.Handler, readTimeout, writeTimeout time.Duration) http.Handler {
	if handler == nil || readTimeout == 0 && writeTimeout == 0 {
		return handler
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controller := http.NewResponseController(w)
		if readTimeout > 0 {
			_ = controller.SetReadDeadline(time.Now().Add(readTimeout))
		}
		if writeTimeout > 0 {
			_ = controller.SetWriteDeadline(time.Now().Add(writeTimeout))
		}
		handler.ServeHTTP(w, r)
	})
}

func validGatewaySegment(segment string) bool {
	return segment != "" && segment != "." && segment != ".." && !strings.ContainsAny(segment, "/\\?#% \t\r\n")
}
