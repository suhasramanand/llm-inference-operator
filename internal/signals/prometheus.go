package signals

import (
	"context"
	"errors"
)

// PrometheusAdapter is a production-shaped placeholder.
// v1 local focuses on FakeConfigMapAdapter for determinism on laptops.
type PrometheusAdapter struct {
	BaseURL string
}

func (a *PrometheusAdapter) Read(ctx context.Context) (Signals, error) {
	return Signals{}, errors.New("prometheus adapter not implemented in v1")
}
