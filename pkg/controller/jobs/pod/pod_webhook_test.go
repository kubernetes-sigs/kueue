/*
Copyright 2023 The Kubernetes Authors.

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

package pod

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	rayjobapi "github.com/ray-project/kuberay/ray-operator/apis/ray/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	_ "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs"
	_ "sigs.k8s.io/kueue/pkg/controller/jobs/mpijob"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestDefault(t *testing.T) {
	defaultNamespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-ns",
			Labels: map[string]string{
				"kubernetes.io/metadata.name": "test-ns",
			},
		},
	}

	defaultNamespaceSelector := &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{
				Key:      "kubernetes.io/metadata.name",
				Operator: metav1.LabelSelectorOpNotIn,
				Values:   []string{"kube-system"},
			},
		},
	}

	testCases := map[string]struct {
		initObjects                []client.Object
		pod                        *corev1.Pod
		manageJobsWithoutQueueName bool
		namespaceSelector          *metav1.LabelSelector
		podSelector                *metav1.LabelSelector
		want                       *corev1.Pod
		wantError                  error
	}{
		"pod with queue nil ns selector": {
			initObjects: []client.Object{defaultNamespace},
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				Obj(),
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				Obj(),
		},
		"pod with queue matching ns selector": {
			initObjects: []client.Object{defaultNamespace},
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				Obj(),
			namespaceSelector: defaultNamespaceSelector,
			podSelector:       &metav1.LabelSelector{},
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				Label("kueue.x-k8s.io/managed", "true").
				KueueSchedulingGate().
				KueueFinalizer().
				Obj(),
		},
		"pod without queue matching ns selector manage jobs without queue name": {
			initObjects: []client.Object{defaultNamespace},
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Obj(),
			manageJobsWithoutQueueName: true,
			namespaceSelector:          defaultNamespaceSelector,
			podSelector:                &metav1.LabelSelector{},
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Label("kueue.x-k8s.io/managed", "true").
				KueueSchedulingGate().
				KueueFinalizer().
				Obj(),
		},
		"pod with owner managed by kueue (Job)": {
			initObjects:       []client.Object{defaultNamespace},
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference("parent-job", batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference("parent-job", batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
		},
		"pod with owner managed by kueue (RayCluster)": {
			initObjects:       []client.Object{defaultNamespace},
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference("parent-ray-cluster", rayjobapi.GroupVersion.WithKind("RayCluster")).
				Obj(),
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference("parent-ray-cluster", rayjobapi.GroupVersion.WithKind("RayCluster")).
				Obj(),
		},
		"pod with owner managed by kueue (MPIJob)": {
			initObjects:       []client.Object{defaultNamespace},
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference(
					"parent-mpi-job",
					schema.GroupVersionKind{Group: "kubeflow.org", Version: "v2beta1", Kind: "MPIJob"},
				).
				Obj(),
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference(
					"parent-mpi-job",
					schema.GroupVersionKind{Group: "kubeflow.org", Version: "v2beta1", Kind: "MPIJob"},
				).
				Obj(),
		},
		"pod with owner managed by kueue (PyTorchJob)": {
			initObjects:       []client.Object{defaultNamespace},
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference(
					"parent-pytorch-job",
					schema.GroupVersionKind{Group: "kubeflow.org", Version: "v1", Kind: "PyTorchJob"},
				).
				Obj(),
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference(
					"parent-pytorch-job",
					schema.GroupVersionKind{Group: "kubeflow.org", Version: "v1", Kind: "PyTorchJob"},
				).
				Obj(),
		},
		"pod with owner managed by kueue (TFJob)": {
			initObjects:       []client.Object{defaultNamespace},
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference(
					"parent-tf-job",
					schema.GroupVersionKind{Group: "kubeflow.org", Version: "v1", Kind: "TFJob"},
				).
				Obj(),
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference(
					"parent-tf-job",
					schema.GroupVersionKind{Group: "kubeflow.org", Version: "v1", Kind: "TFJob"},
				).
				Obj(),
		},
		"pod with owner managed by kueue (XGBoostJob)": {
			initObjects:       []client.Object{defaultNamespace},
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference(
					"parent-xgboost-job",
					schema.GroupVersionKind{Group: "kubeflow.org", Version: "v1", Kind: "XGBoostJob"},
				).
				Obj(),
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference(
					"parent-xgboost-job",
					schema.GroupVersionKind{Group: "kubeflow.org", Version: "v1", Kind: "XGBoostJob"},
				).
				Obj(),
		},
		"pod with owner managed by kueue (PaddleJob)": {
			initObjects:       []client.Object{defaultNamespace},
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference(
					"parent-paddle-job",
					schema.GroupVersionKind{Group: "kubeflow.org", Version: "v1", Kind: "PaddleJob"},
				).
				Obj(),
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference(
					"parent-paddle-job",
					schema.GroupVersionKind{Group: "kubeflow.org", Version: "v1", Kind: "PaddleJob"},
				).
				Obj(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			builder := utiltesting.NewClientBuilder()
			builder = builder.WithObjects(tc.initObjects...)
			cli := builder.Build()

			w := &PodWebhook{
				client:                     cli,
				manageJobsWithoutQueueName: tc.manageJobsWithoutQueueName,
				namespaceSelector:          tc.namespaceSelector,
				podSelector:                tc.podSelector,
			}

			ctx, _ := utiltesting.ContextWithLog(t)

			if err := w.Default(ctx, tc.pod); err != nil {
				t.Errorf("failed to set defaults for v1/pod: %s", err)
			}
			if diff := cmp.Diff(tc.want, tc.pod); len(diff) != 0 {
				t.Errorf("Default() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateCreate(t *testing.T) {
	testCases := map[string]struct {
		pod       *corev1.Pod
		wantWarns admission.Warnings
	}{
		"pod owner is managed by kueue": {
			pod: testingpod.MakePod("test-pod", "test-ns").
				Label("kueue.x-k8s.io/managed", "true").
				OwnerReference("parent-job", batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
			wantWarns: admission.Warnings{
				"pod owner is managed by kueue, label 'kueue.x-k8s.io/managed=true' might lead to unexpected behaviour",
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			builder := utiltesting.NewClientBuilder()
			cli := builder.Build()

			w := &PodWebhook{
				client: cli,
			}

			ctx, _ := utiltesting.ContextWithLog(t)

			warns, err := w.ValidateCreate(ctx, tc.pod)
			if err != nil {
				t.Errorf("failed to set validate create for v1/pod: %s", err)
			}
			if diff := cmp.Diff(warns, tc.wantWarns); diff != "" {
				t.Errorf("Expected different list of warnings (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateUpdate(t *testing.T) {
	testCases := map[string]struct {
		oldPod    *corev1.Pod
		newPod    *corev1.Pod
		wantWarns admission.Warnings
	}{
		"pods owner is managed by kueue, managed label is set for both pods": {
			oldPod: testingpod.MakePod("test-pod", "test-ns").
				Label("kueue.x-k8s.io/managed", "true").
				OwnerReference("parent-job", batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
			newPod: testingpod.MakePod("test-pod", "test-ns").
				Label("kueue.x-k8s.io/managed", "true").
				OwnerReference("parent-job", batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
			wantWarns: admission.Warnings{
				"pod owner is managed by kueue, label 'kueue.x-k8s.io/managed=true' might lead to unexpected behaviour",
			},
		},
		"pod owner is managed by kueue, managed label is set for new pod": {
			oldPod: testingpod.MakePod("test-pod", "test-ns").
				OwnerReference("parent-job", batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
			newPod: testingpod.MakePod("test-pod", "test-ns").
				Label("kueue.x-k8s.io/managed", "true").
				OwnerReference("parent-job", batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
			wantWarns: admission.Warnings{
				"pod owner is managed by kueue, label 'kueue.x-k8s.io/managed=true' might lead to unexpected behaviour",
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			builder := utiltesting.NewClientBuilder()
			cli := builder.Build()

			w := &PodWebhook{
				client: cli,
			}

			ctx, _ := utiltesting.ContextWithLog(t)

			warns, err := w.ValidateUpdate(ctx, tc.oldPod, tc.newPod)
			if err != nil {
				t.Errorf("failed to set validate create for v1/pod: %s", err)
			}
			if diff := cmp.Diff(warns, tc.wantWarns); diff != "" {
				t.Errorf("Expected different list of warnings (-want,+got):\n%s", diff)
			}
		})
	}
}
