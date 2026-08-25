package appports

import "context"

// TransportLifecycleStage is a grouped lifecycle contribution for transport runtimes.
type TransportLifecycleStage struct {
	Name  string
	Start func(context.Context) error
	Stop  func(context.Context) error
}
