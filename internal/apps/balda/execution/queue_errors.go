package execution

import "github.com/normahq/balda/internal/apps/balda/actorcmd"

var ErrCommandQueueFull = actorcmd.ErrCommandQueueFull

func IsCommandQueueFull(err error) bool { return actorcmd.IsCommandQueueFull(err) }
