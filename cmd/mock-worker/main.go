package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type response struct {
	ConversationID string `json:"conversationId"`
	KVShard        string `json:"kvShard"`
	Role           string `json:"role"`
	Pod            string `json:"pod"`
	Message        string `json:"message"`
}

func main() {
	var listen string
	flag.StringVar(&listen, "listen", ":8000", "listen address")
	flag.Parse()

	role := os.Getenv("INFERENCE_ROLE")
	if role == "" {
		role = "decode"
	}

	reqs := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_worker_requests_total",
		Help: "Total requests handled by the mock worker.",
	}, []string{"role"})
	prometheus.MustRegister(reqs)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		reqs.WithLabelValues(role).Inc()

		convID := r.Header.Get("X-Conversation-Id")
		if convID == "" {
			convID = "generated-by-router"
		}
		kvShard := r.Header.Get("X-KV-Shard")
		if kvShard == "" {
			kvShard = "0"
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response{
			ConversationID: convID,
			KVShard:        kvShard,
			Role:           role,
			Pod:            hostname(),
			Message:        fmt.Sprintf("mock worker response (role=%s, shard=%s)", role, kvShard),
		})
	})

	if err := http.ListenAndServe(listen, mux); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}
