package execution

import "github.com/baldaworks/balda/internal/apps/balda/actorcmd"

var ErrCommandQueueFull = actorcmd.ErrCommandQueueFull

func IsCommandQueueFull(err error) bool { return actorcmd.IsCommandQueueFull(err) }
