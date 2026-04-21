package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/suhasreddybr/llm-inference-operator/internal/routing"
)

type options struct {
	listenAddr        string
	metricsAddr       string
	shardMapConfigMap string
	hashKey           string
	namespace         string
	decodeServiceDNS  string
	kubeconfig        string
	pollInterval      time.Duration
}

type chatRequest struct {
	ConversationID string `json:"conversationId,omitempty"`
}

func main() {
	var opt options
	flag.StringVar(&opt.listenAddr, "listen", ":8080", "HTTP listen address")
	flag.StringVar(&opt.metricsAddr, "metrics-listen", ":9090", "Metrics listen address")
	flag.StringVar(&opt.shardMapConfigMap, "shardmap-configmap", "", "ConfigMap name containing shardmap.json")
	flag.StringVar(&opt.hashKey, "hash-key", "conversationId", "Request field used for session affinity")
	flag.StringVar(&opt.namespace, "namespace", os.Getenv("NAMESPACE"), "Kubernetes namespace (defaults to in-cluster)")
	flag.StringVar(&opt.decodeServiceDNS, "decode-service-dns", os.Getenv("DECODE_SERVICE_DNS"), "Decode service host:port (in-cluster DNS)")
	flag.StringVar(&opt.kubeconfig, "kubeconfig", "", "Optional kubeconfig (for local running)")
	flag.DurationVar(&opt.pollInterval, "poll-interval", 2*time.Second, "How often to poll shard map ConfigMap")
	flag.Parse()

	if opt.shardMapConfigMap == "" {
		fmt.Fprintln(os.Stderr, "missing --shardmap-configmap")
		os.Exit(2)
	}
	if opt.namespace == "" {
		opt.namespace = "default"
	}
	if opt.decodeServiceDNS == "" {
		opt.decodeServiceDNS = "localhost:8000"
	}

	logger := zap.New(zap.UseDevMode(true))
	ctx := context.Background()

	k8sCfg, err := kubeConfig(opt.kubeconfig)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	k8sClient, err := client.New(k8sCfg, client.Options{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}

	var (
		currentShardMap atomic.Pointer[routing.ShardMap]
		shardMapErrors  = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "llm_router_shardmap_errors_total",
			Help: "Total shardmap refresh errors.",
		})
		routeLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "llm_router_request_seconds",
			Help:    "End-to-end router request latency.",
			Buckets: prometheus.DefBuckets,
		})
		routeTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_router_requests_total",
			Help: "Total router requests.",
		}, []string{"status"})
		chosenShard = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_router_chosen_shard_total",
			Help: "Chosen shard by requests.",
		}, []string{"shard"})
	)
	prometheus.MustRegister(shardMapErrors, routeLatency, routeTotal, chosenShard)

	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		_ = http.ListenAndServe(opt.metricsAddr, mux)
	}()

	go pollShardMap(ctx, logger, k8sClient, opt.namespace, opt.shardMapConfigMap, opt.pollInterval, &currentShardMap, shardMapErrors)

	httpClient := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		defer func() { routeLatency.Observe(time.Since(start).Seconds()) }()

		body, err := io.ReadAll(r.Body)
		if err != nil {
			routeTotal.WithLabelValues("400").Inc()
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		convID := extractConversationID(body)
		if convID == "" {
			convID = newConversationID()
		}

		sm := currentShardMap.Load()
		shardID := int32(0)
		if sm != nil && len(sm.Shards) > 0 {
			shardID = pickShardForConversation(sm, convID)
		}

		chosenShard.WithLabelValues(fmt.Sprintf("%d", shardID)).Inc()

		upstreamURL := fmt.Sprintf("http://%s/v1/chat/completions", opt.decodeServiceDNS)
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, io.NopCloser(bytes.NewReader(body)))
		if err != nil {
			routeTotal.WithLabelValues("500").Inc()
			http.Error(w, "upstream request build failed", http.StatusInternalServerError)
			return
		}
		req.Header.Set("Content-Type", r.Header.Get("Content-Type"))
		req.Header.Set("X-Conversation-Id", convID)
		req.Header.Set("X-KV-Shard", fmt.Sprintf("%d", shardID))

		resp, err := httpClient.Do(req)
		if err != nil {
			routeTotal.WithLabelValues("502").Inc()
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.Header().Set("X-Conversation-Id", convID)
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		routeTotal.WithLabelValues(fmt.Sprintf("%d", resp.StatusCode)).Inc()
	})

	logger.Info("router starting", "listen", opt.listenAddr, "metrics", opt.metricsAddr, "namespace", opt.namespace, "shardmap", opt.shardMapConfigMap, "decodeServiceDNS", opt.decodeServiceDNS)
	if err := http.ListenAndServe(opt.listenAddr, mux); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func kubeConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	return rest.InClusterConfig()
}

func pollShardMap(
	ctx context.Context,
	log logr.Logger,
	k8sClient client.Client,
	namespace string,
	name string,
	interval time.Duration,
	target *atomic.Pointer[routing.ShardMap],
	errCounter prometheus.Counter,
) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			var cm corev1.ConfigMap
			if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &cm); err != nil {
				errCounter.Inc()
				continue
			}
			raw := []byte(cm.Data["shardmap.json"])
			sm, err := routing.ParseShardMapJSON(raw)
			if err != nil {
				errCounter.Inc()
				continue
			}
			target.Store(sm)
		}
	}
}

func extractConversationID(body []byte) string {
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}
	return req.ConversationID
}

func newConversationID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func pickShardForConversation(sm *routing.ShardMap, conversationID string) int32 {
	// v1: stable shard selection by hashing conversation ID into the shard ID space.
	// The operator then maps shardID -> ownerPod for observability and future direct-to-pod routing.
	h := fnv.New32a()
	_, _ = h.Write([]byte(conversationID))
	maxShardID := int32(0)
	for _, s := range sm.Shards {
		if s.ShardID > maxShardID {
			maxShardID = s.ShardID
		}
	}
	if maxShardID <= 0 {
		return 0
	}
	return int32(h.Sum32() % uint32(maxShardID+1))
}
