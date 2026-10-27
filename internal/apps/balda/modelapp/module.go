package modelapp

import "go.uber.org/fx"

// Module provides the command-neutral queued model operation service.
var Module = fx.Module("balda-model", fx.Provide(New))
