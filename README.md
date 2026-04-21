## LLM Inference Operator (v1)

Kubernetes operator that manages an `LLMInferenceCluster` and provides **KV-aware routing semantics** via a Router service plus a published shard map.

### Tech stack

[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![Kubernetes](https://img.shields.io/badge/Kubernetes-Operator-326CE5?logo=kubernetes&logoColor=white)](https://kubernetes.io/)
[![Kubebuilder](https://img.shields.io/badge/Kubebuilder-v4-2F74C0)](https://book.kubebuilder.io/)
[![controller-runtime](https://img.shields.io/badge/controller--runtime-sigs.k8s.io-7B42F6)](https://github.com/kubernetes-sigs/controller-runtime)
[![Prometheus](https://img.shields.io/badge/Prometheus-Metrics-E6522C?logo=prometheus&logoColor=white)](https://prometheus.io/)
[![Grafana](https://img.shields.io/badge/Grafana-Dashboards-F46800?logo=grafana&logoColor=white)](https://grafana.com/)
[![kind](https://img.shields.io/badge/kind-Local%20K8s-2E6CDA)](https://kind.sigs.k8s.io/)

This repo’s v1 is intentionally **kind/minikube friendly** (no GPUs required) and uses a lightweight **mock worker** to make the full control-plane + routing loop demonstrable on a laptop.

### Architecture (control plane vs data plane)

```mermaid
flowchart TB
  subgraph controlPlane [ControlPlane]
    CRD[CRD:LLMInferenceCluster] --> APIServer[KubernetesAPIServer]
    Operator[Operator:controller-runtime] --> APIServer
    Operator --> Status[CRStatus+Conditions]
    Operator --> ShardMap[ConfigMap:ShardMap]
    Operator --> SignalsCM[ConfigMap:Signals]
    Operator --> RBAC[RBAC:RouterSA+Role+Binding]
  end

  subgraph dataPlane [DataPlane]
    Client[Client] --> Router[RouterService]
    Router --> DecodeSvc[Service:Decode]
    Router --> PrefillSvc[Service:Prefill]
    DecodeSvc --> DecodePods[Pods:DecodeWorkers]
    PrefillSvc --> PrefillPods[Pods:PrefillWorkers]
  end

  subgraph observability [Observability]
    Prom[Prometheus] --> Graf[Grafana]
  end

  ShardMap --> Router
  SignalsCM --> Operator
  Router --> Prom
  DecodePods --> Prom
  PrefillPods --> Prom
```

### What you get in v1 (local, mock-friendly)

- **CRD + controller-runtime reconciler**: creates/patches Deployments/Services/ConfigMaps, updates status + conditions, uses a finalizer.
- **Prefill vs decode split**: separate Services/Deployments for each role.
- **KV shard metadata plane**:
  - Operator publishes `*-shardmap` ConfigMap (`shardmap.json`).
  - Operator also publishes `status.shards[]` with shard ownership.
- **KV-aware routing surface**:
  - Router reads shard map from the ConfigMap, applies **session affinity** (`conversationId → shard`), and forwards to decode.
  - Router injects `X-Conversation-Id` and `X-KV-Shard` headers (mock worker echoes them back).
- **Autoscaling signals (mocked)**:
  - When `spec.autoscaling.enabled=true`, operator reads `*-signals` ConfigMap and scales **decode** replicas.
- **Observability**:
  - Router + mock worker expose Prometheus metrics.
  - `deploy/observability/` provides Prometheus + Grafana for kind.

### What the same architecture is meant to do in production

Even though v1 uses mocks, the control-plane shapes are designed to map directly to a real GPU deployment:

- **Inference runtime**: replace the mock worker image with **vLLM** (or TensorRT-LLM) pods, and (optionally) add Triton for multi-model.
- **GPU layer**: run on nodes managed by **NVIDIA GPU Operator + Device Plugin**; optionally use **MIG** for partitioning.
- **Signals**: switch from fake ConfigMap signals to a **Prometheus-based signals adapter** scraping real metrics:
  - vLLM metrics (tokens/sec, queue depth, cache events)
  - DCGM/NVML exporter (GPU memory/utilization pressure)
  - router latency + shard distribution
- **Autoscaling**: drive scaling using **KEDA** (or an operator-owned scaler) on tokens/sec, queue depth, KV hit rate, and GPU memory pressure.
- **Routing correctness**: router maintains stable session affinity and routes to the correct shard owner across rollouts/reschedules (in production, this often becomes **direct-to-pod** or shard-aware endpoint selection).
- **Memory pressure handling**: decode workers report OOM risk; operator triggers eviction/rebalance and updates shard ownership with minimal disruption.

## Quickstart (kind)

Prereqs:
- `docker`, `kubectl`, `kind`
- `kubebuilder` (for generating CRDs locally)

Create a kind cluster:

```bash
./hack/kind-create.sh
```

Build & load images into kind:

```bash
./hack/kind-load-images.sh
```

Install CRDs + deploy operator:

```bash
./hack/install-kind.sh
```

Apply the sample cluster:

```bash
kubectl apply -f config/samples/inference_v1alpha1_llminferencecluster.yaml
```

Port-forward the router and call it:

```bash
kubectl port-forward svc/llminferencecluster-sample-router 8080:8080
curl -s -X POST localhost:8080/v1/chat/completions -H 'content-type: application/json' \
  -d '{"conversationId":"demo-1","messages":[{"role":"user","content":"hi"}]}' | jq .
```

Simulate queue pressure to trigger decode scaling:

```bash
kubectl patch configmap llminferencecluster-sample-signals -p '{"data":{"queueDepth":"250"}}'
kubectl get deploy llminferencecluster-sample-decode -w
```

## Observability (Prometheus/Grafana)

```bash
kubectl apply -f deploy/observability/prometheus.yaml
kubectl apply -f deploy/observability/grafana.yaml
kubectl -n llm-observability port-forward svc/grafana 3000:3000
```

Grafana should be available at `http://localhost:3000` with an auto-provisioned Prometheus datasource.
