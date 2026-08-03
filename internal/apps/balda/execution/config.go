package execution

import (
	"strings"

	"github.com/normahq/balda/internal/apps/balda/actorcmd"
)

const QueueModeInterrupt = actorcmd.QueueModeInterrupt

const (
	DefaultCommandStream          = "BALDA_COMMANDS"
	DefaultCommandConsumer        = "BALDA_WORKER_COMMANDS"
	DefaultEventStream            = "BALDA_EVENTS"
	DefaultEventProjectorConsumer = "BALDA_EVENT_PROJECTOR"
	DefaultDLQStream              = "BALDA_DLQ"
	DefaultSessionMemoryStream    = "BALDA_SESSION_MEMORY"
	DefaultSessionMemoryConsumer  = "BALDA_SESSION_MEMORY_WORKER"
)

type Config struct {
	Commands CommandConfig
	Events   EventStreamConfig
	DLQ      DLQConfig
	Memory   SessionMemoryConfig
}

type CommandConfig struct {
	Stream        string
	Consumer      string
	AckWait       string
	MaxDeliver    int
	MaxAckPending int
	FetchBatch    int
	FetchWait     string
}

type EventStreamConfig struct {
	Stream string
}

type DLQConfig struct {
	Stream string
}

type SessionMemoryConfig struct {
	Stream          string
	Consumer        string
	AckWait         string
	FetchWait       string
	PublishTimeout  string
	PublishAttempts int
}

func (c Config) Normalized() (Config, error) {
	c.Commands = c.Commands.Normalized()
	c.Events = c.Events.Normalized()
	c.DLQ = c.DLQ.Normalized()
	c.Memory = c.Memory.Normalized()
	return c, nil
}

func (c CommandConfig) Normalized() CommandConfig {
	out := c
	if strings.TrimSpace(out.Stream) == "" {
		out.Stream = DefaultCommandStream
	}
	if strings.TrimSpace(out.Consumer) == "" {
		out.Consumer = DefaultCommandConsumer
	}
	if strings.TrimSpace(out.AckWait) == "" {
		out.AckWait = "5m"
	}
	if out.MaxDeliver <= 0 {
		out.MaxDeliver = 5
	}
	if out.MaxAckPending <= 0 {
		out.MaxAckPending = 64
	}
	if out.FetchBatch <= 0 {
		out.FetchBatch = 16
	}
	if out.FetchBatch > out.MaxAckPending {
		out.FetchBatch = out.MaxAckPending
	}
	if strings.TrimSpace(out.FetchWait) == "" {
		out.FetchWait = "1s"
	}
	return out
}

func (c EventStreamConfig) Normalized() EventStreamConfig {
	if strings.TrimSpace(c.Stream) == "" {
		c.Stream = DefaultEventStream
	}
	return c
}

func (c DLQConfig) Normalized() DLQConfig {
	if strings.TrimSpace(c.Stream) == "" {
		c.Stream = DefaultDLQStream
	}
	return c
}

func (c SessionMemoryConfig) Normalized() SessionMemoryConfig {
	out := c
	if strings.TrimSpace(out.Stream) == "" {
		out.Stream = DefaultSessionMemoryStream
	}
	if strings.TrimSpace(out.Consumer) == "" {
		out.Consumer = DefaultSessionMemoryConsumer
	}
	if strings.TrimSpace(out.AckWait) == "" {
		out.AckWait = "5m"
	}
	if strings.TrimSpace(out.FetchWait) == "" {
		out.FetchWait = "1s"
	}
	if strings.TrimSpace(out.PublishTimeout) == "" {
		out.PublishTimeout = "2s"
	}
	if out.PublishAttempts <= 0 {
		out.PublishAttempts = 3
	}
	return out
}
