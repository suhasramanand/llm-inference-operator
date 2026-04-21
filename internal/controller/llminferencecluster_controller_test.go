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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	inferencev1alpha1 "github.com/suhasreddybr/llm-inference-operator/api/v1alpha1"
)

var _ = Describe("LLMInferenceCluster Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default", // TODO(user):Modify as needed
		}
		llminferencecluster := &inferencev1alpha1.LLMInferenceCluster{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind LLMInferenceCluster")
			err := k8sClient.Get(ctx, typeNamespacedName, llminferencecluster)
			if err != nil && errors.IsNotFound(err) {
				resource := &inferencev1alpha1.LLMInferenceCluster{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: inferencev1alpha1.LLMInferenceClusterSpec{
						Model: inferencev1alpha1.ModelSpec{
							Name:  "mock-model",
							Image: "ghcr.io/suhasreddybr/llm-inference-mock-worker:dev",
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &inferencev1alpha1.LLMInferenceCluster{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if errors.IsNotFound(err) {
				return
			}
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance LLMInferenceCluster")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			By("Reconciling once to clear finalizers")
			controllerReconciler := &LLMInferenceClusterReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			_, _ = controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})

			Eventually(func() bool {
				err := k8sClient.Get(ctx, typeNamespacedName, &inferencev1alpha1.LLMInferenceCluster{})
				return errors.IsNotFound(err)
			}).Should(BeTrue())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &LLMInferenceClusterReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			// TODO(user): Add more specific assertions depending on your controller's reconciliation logic.
			// Example: If you expect a certain status condition after reconciliation, verify it here.
		})
	})
})
