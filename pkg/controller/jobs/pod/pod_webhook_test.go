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
	"github.com/google/go-cmp/cmp/cmpopts"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
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
				OwnerReference("parent-ray-cluster", rayv1.GroupVersion.WithKind("RayCluster")).
				Obj(),
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference("parent-ray-cluster", rayv1.GroupVersion.WithKind("RayCluster")).
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
		"pod with a group name label, but without group total count label": {
			initObjects:       []client.Object{defaultNamespace},
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				Group("test-group").
				Obj(),
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				Group("test-group").
				RoleHash("90ce3e8a").
				Label("kueue.x-k8s.io/managed", "true").
				KueueSchedulingGate().
				KueueFinalizer().
				Obj(),
		},
		"pod with a group name label": {
			initObjects:       []client.Object{defaultNamespace},
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				Group("test-group").
				Obj(),
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				Group("test-group").
				RoleHash("90ce3e8a").
				Label("kueue.x-k8s.io/managed", "true").
				KueueSchedulingGate().
				KueueFinalizer().
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

func TestGetRoleHash(t *testing.T) {
	testCases := map[string]struct {
		pods []*Pod
		// If true, hash for all the pods in test should be equal
		wantEqualHash bool
		wantErr       error
	}{
		"kueue.x-k8s.io/* labels shouldn't affect the role": {
			pods: []*Pod{
				{pod: *testingpod.MakePod("pod1", "test-ns").
					Label("kueue.x-k8s.io/managed", "true").
					Obj()},
				{pod: *testingpod.MakePod("pod2", "test-ns").
					Obj()},
			},
			wantEqualHash: true,
		},
		"volume name shouldn't affect the role": {
			pods: []*Pod{
				{pod: *testingpod.MakePod("pod1", "test-ns").
					Volume(corev1.Volume{
						Name: "volume1",
					}).
					Obj()},
				{pod: *testingpod.MakePod("pod1", "test-ns").
					Volume(corev1.Volume{
						Name: "volume2",
					}).
					Obj()},
			},
			wantEqualHash: true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {

			var previousHash string
			for i := range tc.pods {
				hash, err := getRoleHash(tc.pods[i].pod)

				if diff := cmp.Diff(tc.wantErr, err); diff != "" {
					t.Errorf("Unexpected error (-want,+got):\n%s", diff)
				}

				if previousHash != "" {
					if tc.wantEqualHash {
						if previousHash != hash {
							t.Errorf("Hash of pod shapes shouldn't be different %s!=%s", previousHash, hash)
						}
					} else {
						if previousHash == hash {
							t.Errorf("Hash of pod shapes shouldn't be equal %s==%s", previousHash, hash)
						}
					}
				}

				previousHash = hash
			}
		})
	}
}

func TestValidateCreate(t *testing.T) {
	testCases := map[string]struct {
		pod       *corev1.Pod
		wantErr   error
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
		"pod with group name and no group total count": {
			pod: testingpod.MakePod("test-pod", "test-ns").
				Label("kueue.x-k8s.io/managed", "true").
				Group("test-group").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "metadata.annotations[kueue.x-k8s.io/pod-group-total-count]",
				},
			}.ToAggregate(),
		},
		"pod with group total count and no group name": {
			pod: testingpod.MakePod("test-pod", "test-ns").
				Label("kueue.x-k8s.io/managed", "true").
				GroupTotalCount("3").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "metadata.labels[kueue.x-k8s.io/pod-group-name]",
				},
			}.ToAggregate(),
		},
		"pod with 0 group total count": {
			pod: testingpod.MakePod("test-pod", "test-ns").
				Label("kueue.x-k8s.io/managed", "true").
				Group("test-group").
				GroupTotalCount("0").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.annotations[kueue.x-k8s.io/pod-group-total-count]",
				},
			}.ToAggregate(),
		},
		"pod with empty group total count": {
			pod: testingpod.MakePod("test-pod", "test-ns").
				Label("kueue.x-k8s.io/managed", "true").
				Group("test-group").
				GroupTotalCount("").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.annotations[kueue.x-k8s.io/pod-group-total-count]",
				},
			}.ToAggregate(),
		},
		"pod with incorrect group name": {
			pod: testingpod.MakePod("test-pod", "test-ns").
				Label("kueue.x-k8s.io/managed", "true").
				Group("notAdns1123Subdomain*").
				GroupTotalCount("2").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.labels[kueue.x-k8s.io/pod-group-name]",
				},
			}.ToAggregate(),
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
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
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
		wantErr   error
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
		"pod with group name and no group total count": {
			oldPod: testingpod.MakePod("test-pod", "test-ns").Group("test-group").Obj(),
			newPod: testingpod.MakePod("test-pod", "test-ns").
				Label("kueue.x-k8s.io/managed", "true").
				Group("test-group").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "metadata.annotations[kueue.x-k8s.io/pod-group-total-count]",
				},
			}.ToAggregate(),
		},
		"pod with 0 group total count": {
			oldPod: testingpod.MakePod("test-pod", "test-ns").Group("test-group").Obj(),
			newPod: testingpod.MakePod("test-pod", "test-ns").
				Label("kueue.x-k8s.io/managed", "true").
				Group("test-group").
				GroupTotalCount("0").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.annotations[kueue.x-k8s.io/pod-group-total-count]",
				},
			}.ToAggregate(),
		},
		"pod with empty group total count": {
			oldPod: testingpod.MakePod("test-pod", "test-ns").Group("test-group").Obj(),
			newPod: testingpod.MakePod("test-pod", "test-ns").
				Label("kueue.x-k8s.io/managed", "true").
				Group("test-group").
				GroupTotalCount("").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.annotations[kueue.x-k8s.io/pod-group-total-count]",
				},
			}.ToAggregate(),
		},
		"pod group name is changed": {
			oldPod: testingpod.MakePod("test-pod", "test-ns").
				Group("test-group").
				GroupTotalCount("2").
				Obj(),
			newPod: testingpod.MakePod("test-pod", "test-ns").
				Group("test-group-new").
				GroupTotalCount("2").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.labels[kueue.x-k8s.io/pod-group-name]",
				},
			}.ToAggregate(),
		},
		"retriable in group annotation is removed": {
			oldPod: testingpod.MakePod("test-pod", "test-ns").
				Group("test-group").
				GroupTotalCount("2").
				Annotation("kueue.x-k8s.io/retriable-in-group", "false").
				Obj(),
			newPod: testingpod.MakePod("test-pod", "test-ns").
				Group("test-group").
				GroupTotalCount("2").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeForbidden,
					Field: "metadata.annotations[kueue.x-k8s.io/retriable-in-group]",
				},
			}.ToAggregate(),
		},
		"retriable in group annotation is changed from false to true": {
			oldPod: testingpod.MakePod("test-pod", "test-ns").
				Group("test-group").
				GroupTotalCount("2").
				Annotation("kueue.x-k8s.io/retriable-in-group", "false").
				Obj(),
			newPod: testingpod.MakePod("test-pod", "test-ns").
				Group("test-group").
				GroupTotalCount("2").
				Annotation("kueue.x-k8s.io/retriable-in-group", "true").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeForbidden,
					Field: "metadata.annotations[kueue.x-k8s.io/retriable-in-group]",
				},
			}.ToAggregate(),
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
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(warns, tc.wantWarns); diff != "" {
				t.Errorf("Expected different list of warnings (-want,+got):\n%s", diff)
			}
		})
	}
}
