package signals

import (
	"context"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type FakeConfigMapAdapter struct {
	Client    client.Client
	Namespace string
	Name      string
}

func (a *FakeConfigMapAdapter) Read(ctx context.Context) (Signals, error) {
	var cm corev1.ConfigMap
	if err := a.Client.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: a.Name}, &cm); err != nil {
		return Signals{}, err
	}
	return Signals{
		QueueDepth:      parseFloat(cm.Data["queueDepth"]),
		TokensPerSecond: parseFloat(cm.Data["tokensPerSecond"]),
		MemPressure:     parseFloat(cm.Data["memPressure"]),
	}, nil
}

func parseFloat(v string) float64 {
	if v == "" {
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0
	}
	return f
}
