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

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	inferencev1alpha1 "github.com/suhasreddybr/llm-inference-operator/api/v1alpha1"
	"github.com/suhasreddybr/llm-inference-operator/internal/routing"
	"github.com/suhasreddybr/llm-inference-operator/internal/signals"
)

const (
	llmInferenceClusterFinalizer = "inference.llm.io/finalizer"

	conditionReady    = "Ready"
	conditionDegraded = "Degraded"
	conditionScaling  = "Scaling"

	rolePrefill = "prefill"
	roleDecode  = "decode"
)

// LLMInferenceClusterReconciler reconciles a LLMInferenceCluster object
type LLMInferenceClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=inference.llm.io,resources=llminferenceclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=inference.llm.io,resources=llminferenceclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=inference.llm.io,resources=llminferenceclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;configmaps;pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *LLMInferenceClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var cluster inferencev1alpha1.LLMInferenceCluster
	if err := r.Get(ctx, req.NamespacedName, &cluster); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	cluster = *cluster.DeepCopy()
	original := cluster.DeepCopy()

	if cluster.Spec.Routing.Mode == "" {
		cluster.Spec.Routing.Mode = inferencev1alpha1.RoutingModeSessionAffinity
	}
	if cluster.Spec.Routing.HashKey == nil {
		defaultHashKey := "conversationId"
		cluster.Spec.Routing.HashKey = &defaultHashKey
	}
	if cluster.Spec.Routing.RouterReplicas == nil {
		var one int32 = 1
		cluster.Spec.Routing.RouterReplicas = &one
	}
	if cluster.Spec.Runtime.Decode.ServicePort == nil {
		var p int32 = 8000
		cluster.Spec.Runtime.Decode.ServicePort = &p
	}
	if cluster.Spec.Runtime.Prefill.ServicePort == nil {
		var p int32 = 8001
		cluster.Spec.Runtime.Prefill.ServicePort = &p
	}
	if cluster.Spec.KVCache.Strategy == "" {
		cluster.Spec.KVCache.Strategy = inferencev1alpha1.KVCacheStrategySharded
	}
	if cluster.Spec.KVCache.NumShards == nil {
		var shards int32 = 8
		cluster.Spec.KVCache.NumShards = &shards
	}
	if cluster.Spec.KVCache.EvictionPolicy == "" {
		cluster.Spec.KVCache.EvictionPolicy = inferencev1alpha1.EvictionPolicyLRU
	}
	if cluster.Spec.KVCache.WarmCache == "" {
		cluster.Spec.KVCache.WarmCache = inferencev1alpha1.WarmCacheNone
	}

	// Persist defaults back to spec.
	if err := r.Patch(ctx, &cluster, client.MergeFrom(original)); err != nil {
		return ctrl.Result{}, err
	}

	if !cluster.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&cluster, llmInferenceClusterFinalizer) {
			controllerutil.RemoveFinalizer(&cluster, llmInferenceClusterFinalizer)
			if err := r.Update(ctx, &cluster); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(&cluster, llmInferenceClusterFinalizer) {
		controllerutil.AddFinalizer(&cluster, llmInferenceClusterFinalizer)
		if err := r.Update(ctx, &cluster); err != nil {
			return ctrl.Result{}, err
		}
	}

	labels := baseLabels(&cluster)

	decodeSvcName := fmt.Sprintf("%s-decode", cluster.Name)
	prefillSvcName := fmt.Sprintf("%s-prefill", cluster.Name)
	routerSvcName := fmt.Sprintf("%s-router", cluster.Name)
	shardMapName := fmt.Sprintf("%s-shardmap", cluster.Name)
	signalsCMName := fmt.Sprintf("%s-signals", cluster.Name)

	var decodeOverrideReplicas *int32
	if cluster.Spec.Autoscaling.Enabled != nil && *cluster.Spec.Autoscaling.Enabled {
		if err := r.reconcileSignalsConfigMap(ctx, &cluster, signalsCMName, labels); err != nil {
			return r.requeueDegraded(ctx, &cluster, err)
		}
		replicas, scaling, err := r.computeDecodeReplicas(ctx, &cluster, signalsCMName)
		if err != nil {
			return r.requeueDegraded(ctx, &cluster, err)
		}
		decodeOverrideReplicas = &replicas
		if scaling {
			setCondition(&cluster, metav1.Condition{
				Type:               conditionScaling,
				Status:             metav1.ConditionTrue,
				Reason:             "Autoscaling",
				Message:            fmt.Sprintf("Autoscaling decode replicas to %d", replicas),
				ObservedGeneration: cluster.Generation,
				LastTransitionTime: metav1.Now(),
			})
		} else {
			setCondition(&cluster, metav1.Condition{
				Type:               conditionScaling,
				Status:             metav1.ConditionFalse,
				Reason:             "Stable",
				Message:            "No autoscaling change required",
				ObservedGeneration: cluster.Generation,
				LastTransitionTime: metav1.Now(),
			})
		}
	}

	if err := r.reconcileRoleService(ctx, &cluster, roleDecode, decodeSvcName, *cluster.Spec.Runtime.Decode.ServicePort, labels); err != nil {
		return r.requeueDegraded(ctx, &cluster, err)
	}
	if err := r.reconcileRoleService(ctx, &cluster, rolePrefill, prefillSvcName, *cluster.Spec.Runtime.Prefill.ServicePort, labels); err != nil {
		return r.requeueDegraded(ctx, &cluster, err)
	}
	if err := r.reconcileRouterService(ctx, &cluster, routerSvcName, labels); err != nil {
		return r.requeueDegraded(ctx, &cluster, err)
	}

	routerSAName := fmt.Sprintf("%s-router", cluster.Name)
	if err := r.reconcileRouterRBAC(ctx, &cluster, routerSAName, labels); err != nil {
		return r.requeueDegraded(ctx, &cluster, err)
	}

	if err := r.reconcileRoleDeployment(ctx, &cluster, roleDecode, decodeSvcName, labels, decodeOverrideReplicas); err != nil {
		return r.requeueDegraded(ctx, &cluster, err)
	}
	if err := r.reconcileRoleDeployment(ctx, &cluster, rolePrefill, prefillSvcName, labels, nil); err != nil {
		return r.requeueDegraded(ctx, &cluster, err)
	}
	if err := r.reconcileRouterDeployment(ctx, &cluster, routerSvcName, shardMapName, routerSAName, labels); err != nil {
		return r.requeueDegraded(ctx, &cluster, err)
	}

	shards, shardMapJSON, err := r.computeShardMap(ctx, &cluster, labels)
	if err != nil {
		return r.requeueDegraded(ctx, &cluster, err)
	}
	cluster.Status.Shards = shards

	if err := r.reconcileShardMapConfigMap(ctx, &cluster, shardMapName, labels, shardMapJSON); err != nil {
		return r.requeueDegraded(ctx, &cluster, err)
	}

	// Update status.
	cluster.Status.ObservedGeneration = cluster.Generation
	cluster.Status.Endpoints = inferencev1alpha1.ClusterEndpointsStatus{
		RouterURL:      fmt.Sprintf("http://%s.%s.svc.cluster.local:8080", routerSvcName, cluster.Namespace),
		DecodeService:  fmt.Sprintf("%s.%s.svc.cluster.local:%d", decodeSvcName, cluster.Namespace, *cluster.Spec.Runtime.Decode.ServicePort),
		PrefillService: fmt.Sprintf("%s.%s.svc.cluster.local:%d", prefillSvcName, cluster.Namespace, *cluster.Spec.Runtime.Prefill.ServicePort),
	}

	setCondition(&cluster, metav1.Condition{
		Type:               conditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             "Reconciled",
		Message:            "Resources are reconciled",
		ObservedGeneration: cluster.Generation,
		LastTransitionTime: metav1.Now(),
	})
	setCondition(&cluster, metav1.Condition{
		Type:               conditionDegraded,
		Status:             metav1.ConditionFalse,
		Reason:             "Healthy",
		Message:            "No reconciliation errors",
		ObservedGeneration: cluster.Generation,
		LastTransitionTime: metav1.Now(),
	})

	if err := r.Status().Patch(ctx, &cluster, client.MergeFrom(original)); err != nil {
		log.Error(err, "status update failed")
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *LLMInferenceClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&inferencev1alpha1.LLMInferenceCluster{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Named("llminferencecluster").
		Complete(r)
}

func baseLabels(cluster *inferencev1alpha1.LLMInferenceCluster) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":    "llm-inference-operator",
		"app.kubernetes.io/part-of": "llm-inference-operator",
		"inference.llm.io/cluster":  cluster.Name,
	}
}

func roleLabels(base map[string]string, role string) map[string]string {
	out := make(map[string]string, len(base)+1)
	for k, v := range base {
		out[k] = v
	}
	out["inference.llm.io/role"] = role
	return out
}

func setCondition(cluster *inferencev1alpha1.LLMInferenceCluster, cond metav1.Condition) {
	conds := cluster.Status.Conditions
	meta.SetStatusCondition(&conds, cond)
	cluster.Status.Conditions = conds
}

func (r *LLMInferenceClusterReconciler) requeueDegraded(ctx context.Context, cluster *inferencev1alpha1.LLMInferenceCluster, err error) (ctrl.Result, error) {
	setCondition(cluster, metav1.Condition{
		Type:               conditionReady,
		Status:             metav1.ConditionFalse,
		Reason:             "Error",
		Message:            err.Error(),
		ObservedGeneration: cluster.Generation,
		LastTransitionTime: metav1.Now(),
	})
	setCondition(cluster, metav1.Condition{
		Type:               conditionDegraded,
		Status:             metav1.ConditionTrue,
		Reason:             "ReconcileError",
		Message:            err.Error(),
		ObservedGeneration: cluster.Generation,
		LastTransitionTime: metav1.Now(),
	})
	_ = r.Status().Update(ctx, cluster)
	return ctrl.Result{RequeueAfter: 5 * time.Second}, err
}

func (r *LLMInferenceClusterReconciler) reconcileRoleService(
	ctx context.Context,
	cluster *inferencev1alpha1.LLMInferenceCluster,
	role string,
	name string,
	port int32,
	base map[string]string,
) error {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cluster.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		svc.Labels = roleLabels(base, role)
		svc.Spec.Selector = roleLabels(base, role)
		svc.Spec.Ports = []corev1.ServicePort{{
			Name:       "http",
			Port:       port,
			Protocol:   corev1.ProtocolTCP,
			TargetPort: intstrFromInt(int(port)),
		}}
		return controllerutil.SetControllerReference(cluster, svc, r.Scheme)
	})
	return err
}

func (r *LLMInferenceClusterReconciler) reconcileRouterService(
	ctx context.Context,
	cluster *inferencev1alpha1.LLMInferenceCluster,
	name string,
	base map[string]string,
) error {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cluster.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		svc.Labels = roleLabels(base, "router")
		svc.Spec.Selector = roleLabels(base, "router")
		svc.Spec.Ports = []corev1.ServicePort{
			{Name: "http", Port: 8080, Protocol: corev1.ProtocolTCP, TargetPort: intstrFromInt(8080)},
			{Name: "metrics", Port: 9090, Protocol: corev1.ProtocolTCP, TargetPort: intstrFromInt(9090)},
		}
		return controllerutil.SetControllerReference(cluster, svc, r.Scheme)
	})
	return err
}

func (r *LLMInferenceClusterReconciler) reconcileRoleDeployment(
	ctx context.Context,
	cluster *inferencev1alpha1.LLMInferenceCluster,
	role string,
	serviceName string,
	base map[string]string,
	overrideReplicas *int32,
) error {
	name := fmt.Sprintf("%s-%s", cluster.Name, role)
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cluster.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, dep, func() error {
		labels := roleLabels(base, role)
		dep.Labels = labels
		replicas := int32(1)
		if role == roleDecode && cluster.Spec.Runtime.Decode.Replicas != nil {
			replicas = *cluster.Spec.Runtime.Decode.Replicas
		}
		if role == rolePrefill && cluster.Spec.Runtime.Prefill.Replicas != nil {
			replicas = *cluster.Spec.Runtime.Prefill.Replicas
		}
		if role == roleDecode && overrideReplicas != nil {
			replicas = *overrideReplicas
		}
		dep.Spec.Replicas = &replicas
		dep.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		dep.Spec.Template.ObjectMeta.Labels = labels
		if dep.Spec.Template.Spec.Containers == nil {
			dep.Spec.Template.Spec.Containers = []corev1.Container{{Name: "worker"}}
		}
		c := &dep.Spec.Template.Spec.Containers[0]
		c.Name = "worker"
		c.Image = cluster.Spec.Model.Image
		c.ImagePullPolicy = corev1.PullIfNotPresent
		c.Args = cluster.Spec.Model.Args
		c.Env = buildEnvVars(cluster.Spec.Model.Env)
		c.Ports = []corev1.ContainerPort{{Name: "http", ContainerPort: servicePortForRole(cluster, role)}}
		c.Env = append(c.Env,
			corev1.EnvVar{Name: "INFERENCE_ROLE", Value: role},
			corev1.EnvVar{Name: "SERVICE_NAME", Value: serviceName},
			corev1.EnvVar{Name: "KV_SHARDS", Value: fmt.Sprintf("%d", derefInt32(cluster.Spec.KVCache.NumShards, 8))},
		)
		return controllerutil.SetControllerReference(cluster, dep, r.Scheme)
	})
	return err
}

func (r *LLMInferenceClusterReconciler) reconcileRouterDeployment(
	ctx context.Context,
	cluster *inferencev1alpha1.LLMInferenceCluster,
	serviceName string,
	shardMapName string,
	serviceAccountName string,
	base map[string]string,
) error {
	name := fmt.Sprintf("%s-router", cluster.Name)
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cluster.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, dep, func() error {
		labels := roleLabels(base, "router")
		dep.Labels = labels
		dep.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		dep.Spec.Template.ObjectMeta.Labels = labels
		dep.Spec.Replicas = cluster.Spec.Routing.RouterReplicas
		dep.Spec.Template.Spec.ServiceAccountName = serviceAccountName

		if dep.Spec.Template.Spec.Containers == nil {
			dep.Spec.Template.Spec.Containers = []corev1.Container{{Name: "router"}}
		}
		c := &dep.Spec.Template.Spec.Containers[0]
		c.Name = "router"
		c.Image = "llm-inference-router:latest"
		c.ImagePullPolicy = corev1.PullIfNotPresent
		c.Args = []string{
			"--shardmap-configmap", shardMapName,
			"--hash-key", derefString(cluster.Spec.Routing.HashKey, "conversationId"),
		}
		c.Ports = []corev1.ContainerPort{
			{Name: "http", ContainerPort: 8080},
			{Name: "metrics", ContainerPort: 9090},
		}
		c.Env = []corev1.EnvVar{
			{Name: "NAMESPACE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}},
			{Name: "DECODE_SERVICE_DNS", Value: fmt.Sprintf("%s.%s.svc.cluster.local:%d", fmt.Sprintf("%s-decode", cluster.Name), cluster.Namespace, *cluster.Spec.Runtime.Decode.ServicePort)},
			{Name: "PREFILL_SERVICE_DNS", Value: fmt.Sprintf("%s.%s.svc.cluster.local:%d", fmt.Sprintf("%s-prefill", cluster.Name), cluster.Namespace, *cluster.Spec.Runtime.Prefill.ServicePort)},
		}
		return controllerutil.SetControllerReference(cluster, dep, r.Scheme)
	})
	return err
}

func (r *LLMInferenceClusterReconciler) reconcileRouterRBAC(
	ctx context.Context,
	cluster *inferencev1alpha1.LLMInferenceCluster,
	serviceAccountName string,
	base map[string]string,
) error {
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: serviceAccountName, Namespace: cluster.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, sa, func() error {
		sa.Labels = roleLabels(base, "router")
		return controllerutil.SetControllerReference(cluster, sa, r.Scheme)
	}); err != nil {
		return err
	}

	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: serviceAccountName, Namespace: cluster.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, role, func() error {
		role.Labels = roleLabels(base, "router")
		role.Rules = []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"configmaps"},
				Verbs:     []string{"get", "list", "watch"},
			},
		}
		return controllerutil.SetControllerReference(cluster, role, r.Scheme)
	}); err != nil {
		return err
	}

	rb := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: serviceAccountName, Namespace: cluster.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, rb, func() error {
		rb.Labels = roleLabels(base, "router")
		rb.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name}
		rb.Subjects = []rbacv1.Subject{{Kind: "ServiceAccount", Name: sa.Name, Namespace: sa.Namespace}}
		return controllerutil.SetControllerReference(cluster, rb, r.Scheme)
	})
	return err
}

func (r *LLMInferenceClusterReconciler) reconcileShardMapConfigMap(
	ctx context.Context,
	cluster *inferencev1alpha1.LLMInferenceCluster,
	name string,
	base map[string]string,
	shardMapJSON string,
) error {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cluster.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		cm.Labels = roleLabels(base, "shardmap")
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data["shardmap.json"] = shardMapJSON
		return controllerutil.SetControllerReference(cluster, cm, r.Scheme)
	})
	return err
}

func (r *LLMInferenceClusterReconciler) reconcileSignalsConfigMap(
	ctx context.Context,
	cluster *inferencev1alpha1.LLMInferenceCluster,
	name string,
	base map[string]string,
) error {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: cluster.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		cm.Labels = roleLabels(base, "signals")
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		if _, ok := cm.Data["queueDepth"]; !ok {
			cm.Data["queueDepth"] = "0"
		}
		if _, ok := cm.Data["tokensPerSecond"]; !ok {
			cm.Data["tokensPerSecond"] = "0"
		}
		if _, ok := cm.Data["memPressure"]; !ok {
			cm.Data["memPressure"] = "0"
		}
		return controllerutil.SetControllerReference(cluster, cm, r.Scheme)
	})
	return err
}

func (r *LLMInferenceClusterReconciler) computeDecodeReplicas(
	ctx context.Context,
	cluster *inferencev1alpha1.LLMInferenceCluster,
	signalsCMName string,
) (int32, bool, error) {
	current := int32(1)
	if cluster.Spec.Runtime.Decode.Replicas != nil {
		current = *cluster.Spec.Runtime.Decode.Replicas
	}
	min := int32(0)
	if cluster.Spec.Autoscaling.MinReplicas != nil {
		min = *cluster.Spec.Autoscaling.MinReplicas
	}
	max := int32(10)
	if cluster.Spec.Autoscaling.MaxReplicas != nil {
		max = *cluster.Spec.Autoscaling.MaxReplicas
	}
	if max < 1 {
		max = 1
	}
	if current < min {
		current = min
	}
	if current > max {
		current = max
	}

	source := cluster.Spec.Autoscaling.Signals.Source
	if source == "" {
		source = inferencev1alpha1.SignalsSourceFake
	}

	var adapter signals.Adapter
	switch source {
	case inferencev1alpha1.SignalsSourceFake:
		adapter = &signals.FakeConfigMapAdapter{Client: r.Client, Namespace: cluster.Namespace, Name: signalsCMName}
	case inferencev1alpha1.SignalsSourcePrometheus:
		baseURL := ""
		if cluster.Spec.Autoscaling.Signals.PrometheusURL != nil {
			baseURL = *cluster.Spec.Autoscaling.Signals.PrometheusURL
		}
		adapter = &signals.PrometheusAdapter{BaseURL: baseURL}
	default:
		adapter = &signals.FakeConfigMapAdapter{Client: r.Client, Namespace: cluster.Namespace, Name: signalsCMName}
	}

	s, err := adapter.Read(ctx)
	if err != nil {
		return current, false, err
	}

	desired := current
	switch {
	case s.QueueDepth > 100:
		desired = current + 1
	case s.QueueDepth < 10 && current > min:
		desired = current - 1
	}

	if desired < min {
		desired = min
	}
	if desired > max {
		desired = max
	}
	return desired, desired != current, nil
}

type shardMapDoc struct {
	Version int             `json:"version"`
	Shards  []shardMapEntry `json:"shards"`
}

type shardMapEntry struct {
	ShardID  int32  `json:"shardID"`
	Role     string `json:"role"`
	OwnerPod string `json:"ownerPod,omitempty"`
}

func (r *LLMInferenceClusterReconciler) computeShardMap(
	ctx context.Context,
	cluster *inferencev1alpha1.LLMInferenceCluster,
	base map[string]string,
) ([]inferencev1alpha1.ShardStatus, string, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods,
		client.InNamespace(cluster.Namespace),
		client.MatchingLabels(roleLabels(base, roleDecode)),
	); err != nil {
		return nil, "", err
	}

	ready := make([]string, 0, len(pods.Items))
	for _, p := range pods.Items {
		if isPodReady(&p) {
			ready = append(ready, p.Name)
		}
	}
	if len(ready) == 0 {
		for _, p := range pods.Items {
			ready = append(ready, p.Name)
		}
	}

	ring := routing.NewRing(ready)
	numShards := derefInt32(cluster.Spec.KVCache.NumShards, 8)

	now := metav1.Now()
	shards := make([]inferencev1alpha1.ShardStatus, 0, numShards)
	doc := shardMapDoc{Version: 1, Shards: make([]shardMapEntry, 0, numShards)}

	for i := int32(0); i < numShards; i++ {
		owner := ""
		phase := inferencev1alpha1.ShardPhasePending
		if !ring.Empty() {
			owner = ring.Pick(i)
			phase = inferencev1alpha1.ShardPhaseReady
		}

		shards = append(shards, inferencev1alpha1.ShardStatus{
			ShardID:       i,
			Role:          roleDecode,
			OwnerPod:      owner,
			Phase:         phase,
			LastHeartbeat: now,
		})
		doc.Shards = append(doc.Shards, shardMapEntry{ShardID: i, Role: roleDecode, OwnerPod: owner})
	}

	b, err := json.Marshal(doc)
	if err != nil {
		return nil, "", err
	}
	return shards, string(b), nil
}

func isPodReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func buildEnvVars(env []inferencev1alpha1.EnvVar) []corev1.EnvVar {
	out := make([]corev1.EnvVar, 0, len(env))
	for _, e := range env {
		out = append(out, corev1.EnvVar{Name: e.Name, Value: e.Value})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func servicePortForRole(cluster *inferencev1alpha1.LLMInferenceCluster, role string) int32 {
	if role == roleDecode && cluster.Spec.Runtime.Decode.ServicePort != nil {
		return *cluster.Spec.Runtime.Decode.ServicePort
	}
	if role == rolePrefill && cluster.Spec.Runtime.Prefill.ServicePort != nil {
		return *cluster.Spec.Runtime.Prefill.ServicePort
	}
	return 8000
}

func derefInt32(v *int32, def int32) int32 {
	if v == nil {
		return def
	}
	return *v
}

func derefString(v *string, def string) string {
	if v == nil {
		return def
	}
	return *v
}

func intstrFromInt(i int) intstr.IntOrString {
	return intstr.IntOrString{Type: intstr.Int, IntVal: int32(i)}
}
