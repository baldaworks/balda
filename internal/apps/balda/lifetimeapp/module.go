package lifetimeapp

import "go.uber.org/fx"

// Module provides the command-neutral queued fresh lifetime service.
var Module = fx.Module("balda-fresh-lifetime", fx.Provide(New))
