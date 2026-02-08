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

	agentsv1alpha1 "github.com/jcwearn/agent-operator/api/v1alpha1"
)

var _ = Describe("CodingTask Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-codingtask"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}
		codingtask := &agentsv1alpha1.CodingTask{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind CodingTask")
			err := k8sClient.Get(ctx, typeNamespacedName, codingtask)
			if err != nil && errors.IsNotFound(err) {
				resource := &agentsv1alpha1.CodingTask{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: agentsv1alpha1.CodingTaskSpec{
						Source: agentsv1alpha1.TaskSource{
							Type: agentsv1alpha1.TaskSourceChat,
							Chat: &agentsv1alpha1.ChatSource{
								SessionID: "test-session",
								UserID:    "test-user",
							},
						},
						Repository: agentsv1alpha1.RepositorySpec{
							URL:    "https://github.com/example/test-repo.git",
							Branch: "main",
						},
						Prompt: "Fix the test bug",
						AnthropicAPIKeyRef: agentsv1alpha1.SecretReference{
							Name: "anthropic-secret",
							Key:  "api-key",
						},
						GitCredentialsRef: agentsv1alpha1.SecretReference{
							Name: "git-secret",
							Key:  "token",
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &agentsv1alpha1.CodingTask{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance CodingTask")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})

		It("should initialize status to Pending on first reconcile", func() {
			controllerReconciler := &CodingTaskReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify status was initialized.
			updated := &agentsv1alpha1.CodingTask{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, updated)).To(Succeed())
			Expect(updated.Status.Phase).To(Equal(agentsv1alpha1.TaskPhasePending))
			Expect(updated.Status.TotalSteps).To(Equal(5))
		})
	})
})
