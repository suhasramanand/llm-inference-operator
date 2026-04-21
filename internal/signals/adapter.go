package signals

import "context"

type Signals struct {
	QueueDepth      float64
	TokensPerSecond float64
	MemPressure     float64
}

type Adapter interface {
	Read(ctx context.Context) (Signals, error)
}
