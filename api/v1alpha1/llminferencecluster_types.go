/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type EvictionPolicy string

const (
	EvictionPolicyLRU                EvictionPolicy = "lru"
	EvictionPolicyTTL                EvictionPolicy = "ttl"
	EvictionPolicyAttentionAwareStub EvictionPolicy = "attentionAwareStub"
)

type WarmCacheMode string

const (
	WarmCacheNone     WarmCacheMode = "none"
	WarmCacheMemory   WarmCacheMode = "memory"
	WarmCacheDiskStub WarmCacheMode = "diskStub"
)

type KVCacheStrategy string

const (
	KVCacheStrategySharded KVCacheStrategy = "sharded"
)

type RoutingMode string

const (
	RoutingModeSessionAffinity RoutingMode = "sessionAffinity"
)

type SignalsSource string

const (
	SignalsSourceFake       SignalsSource = "fake"
	SignalsSourcePrometheus SignalsSource = "prometheus"
)

type ModelSpec struct {
	// name is a human-friendly model identifier (e.g. "llama3-8b").
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// image is the container image for the inference worker.
	// In local v1, this may point to a mock worker.
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// args are passed to the worker container entrypoint.
	// +optional
	Args []string `json:"args,omitempty"`

	// env are additional environment variables for the worker container.
	// +optional
	Env []EnvVar `json:"env,omitempty"`
}

type EnvVar struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +optional
	Value string `json:"value,omitempty"`
}

type WorkloadRoleSpec struct {
	// replicas is the desired replica count for this role.
	// +kubebuilder:validation:Minimum=0
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// servicePort is the HTTP port exposed by the role Service.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	ServicePort *int32 `json:"servicePort,omitempty"`

	// podLabels are merged into the pod template labels.
	// +optional
	PodLabels map[string]string `json:"podLabels,omitempty"`
}

type RuntimeSpec struct {
	// prefill is the (typically heavier) prefill workload.
	// +optional
	Prefill WorkloadRoleSpec `json:"prefill,omitempty"`

	// decode is the (latency-sensitive) decode workload.
	// +optional
	Decode WorkloadRoleSpec `json:"decode,omitempty"`
}

type KVCacheSpec struct {
	// strategy describes how the cache is distributed.
	// +kubebuilder:validation:Enum=sharded
	// +optional
	Strategy KVCacheStrategy `json:"strategy,omitempty"`

	// numShards is the number of KV shards to publish/track.
	// +kubebuilder:validation:Minimum=1
	// +optional
	NumShards *int32 `json:"numShards,omitempty"`

	// evictionPolicy is the eviction strategy used when under memory pressure.
	// +kubebuilder:validation:Enum=lru;ttl;attentionAwareStub
	// +optional
	EvictionPolicy EvictionPolicy `json:"evictionPolicy,omitempty"`

	// warmCache configures a second-tier cache (local-only in v1).
	// +kubebuilder:validation:Enum=none;memory;diskStub
	// +optional
	WarmCache WarmCacheMode `json:"warmCache,omitempty"`
}

type RoutingSpec struct {
	// mode controls routing semantics (v1: session affinity).
	// +kubebuilder:validation:Enum=sessionAffinity
	// +optional
	Mode RoutingMode `json:"mode,omitempty"`

	// hashKey controls what request field is used to provide stable routing.
	// +kubebuilder:validation:MinLength=1
	// +optional
	HashKey *string `json:"hashKey,omitempty"`

	// routerReplicas is the desired replica count for the router service.
	// +kubebuilder:validation:Minimum=1
	// +optional
	RouterReplicas *int32 `json:"routerReplicas,omitempty"`
}

type AutoscalingSignalsSpec struct {
	// source selects which adapter powers autoscaling decisions.
	// +kubebuilder:validation:Enum=fake;prometheus
	// +optional
	Source SignalsSource `json:"source,omitempty"`

	// prometheusURL is used when source=prometheus.
	// +optional
	PrometheusURL *string `json:"prometheusURL,omitempty"`
}

type AutoscalingSpec struct {
	// enabled controls whether the operator should actively scale workloads.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// minReplicas bounds decode scaling.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MinReplicas *int32 `json:"minReplicas,omitempty"`

	// maxReplicas bounds decode scaling.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxReplicas *int32 `json:"maxReplicas,omitempty"`

	// signals configures the metrics/signal source.
	// +optional
	Signals AutoscalingSignalsSpec `json:"signals,omitempty"`
}

// LLMInferenceClusterSpec defines the desired state of LLMInferenceCluster.
type LLMInferenceClusterSpec struct {
	// model defines the worker image and configuration.
	Model ModelSpec `json:"model"`

	// runtime defines prefill vs decode role sizing.
	// +optional
	Runtime RuntimeSpec `json:"runtime,omitempty"`

	// kvCache configures KV cache strategy and shard tracking.
	// +optional
	KVCache KVCacheSpec `json:"kvCache,omitempty"`

	// routing configures the session-aware router.
	// +optional
	Routing RoutingSpec `json:"routing,omitempty"`

	// autoscaling configures token/queue/pressure-driven scaling.
	// +optional
	Autoscaling AutoscalingSpec `json:"autoscaling,omitempty"`
}

// LLMInferenceClusterStatus defines the observed state of LLMInferenceCluster.
type LLMInferenceClusterStatus struct {
	// observedGeneration is the last generation acted on by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// endpoints summarizes the primary access URLs for the cluster.
	// +optional
	Endpoints ClusterEndpointsStatus `json:"endpoints,omitempty"`

	// shards is the published shard ownership map.
	// +optional
	Shards []ShardStatus `json:"shards,omitempty"`

	// conditions represent the current state of the LLMInferenceCluster resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

type ClusterEndpointsStatus struct {
	// routerURL is the primary endpoint for clients.
	// +optional
	RouterURL string `json:"routerURL,omitempty"`

	// decodeService is the in-cluster Service for decode.
	// +optional
	DecodeService string `json:"decodeService,omitempty"`

	// prefillService is the in-cluster Service for prefill.
	// +optional
	PrefillService string `json:"prefillService,omitempty"`
}

type ShardPhase string

const (
	ShardPhasePending  ShardPhase = "Pending"
	ShardPhaseReady    ShardPhase = "Ready"
	ShardPhaseDegraded ShardPhase = "Degraded"
)

type ShardStatus struct {
	// shardID is the numeric shard identifier.
	// +kubebuilder:validation:Minimum=0
	ShardID int32 `json:"shardID"`

	// role is the workload role owning the shard (prefill/decode).
	// +kubebuilder:validation:Enum=prefill;decode
	Role string `json:"role"`

	// ownerPod is the pod name that currently owns the shard.
	// +optional
	OwnerPod string `json:"ownerPod,omitempty"`

	// phase is the current shard state.
	// +kubebuilder:validation:Enum=Pending;Ready;Degraded
	// +optional
	Phase ShardPhase `json:"phase,omitempty"`

	// lastHeartbeat is updated when the owning pod reports health.
	// +optional
	LastHeartbeat metav1.Time `json:"lastHeartbeat,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// LLMInferenceCluster is the Schema for the llminferenceclusters API
type LLMInferenceCluster struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of LLMInferenceCluster
	// +required
	Spec LLMInferenceClusterSpec `json:"spec"`

	// status defines the observed state of LLMInferenceCluster
	// +optional
	Status LLMInferenceClusterStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// LLMInferenceClusterList contains a list of LLMInferenceCluster
type LLMInferenceClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []LLMInferenceCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&LLMInferenceCluster{}, &LLMInferenceClusterList{})
}
