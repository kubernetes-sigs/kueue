/*
Copyright 2024 The Kubernetes Authors.

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

package list

import (
	"bytes"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	rayutils "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"

	cmdtesting "sigs.k8s.io/kueue/cmd/kueuectl/app/testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/cli-runtime/pkg/resource"
	fakediscovery "k8s.io/client-go/discovery/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	restfake "k8s.io/client-go/rest/fake"

	jobsetapi "sigs.k8s.io/jobset/api/jobset/v1alpha2"
)

func TestPodCmd(t *testing.T) {
	testStartTime := time.Now()

	testCases := []struct {
		name             string
		namespace        string
		objs             []runtime.Object
		job              []runtime.Object
		apiResourceLists []*metav1.APIResourceList
		mapperGVKs       []schema.GroupVersionKind
		args             []string
		wantOut          string
		wantOutErr       string
		wantErr          error
	}{
		{
			name: "list pods for valid batch/job type",
			job: []runtime.Object{
				&batchv1.Job{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-job",
						Namespace: metav1.NamespaceDefault,
						Labels: map[string]string{
							batchv1.JobNameLabel: "test-job",
						},
					},
				},
			},
			objs: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							batchv1.JobNameLabel: "test-job",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "batch/v1",
								Kind:       "Job",
								Name:       "test-job",
							},
						},
					},
					Status: corev1.PodStatus{
						Phase: "RUNNING",
					},
				}, &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-2",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							batchv1.JobNameLabel: "test-job",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "batch/v1",
								Kind:       "Job",
								Name:       "test-job",
							},
						},
					},
					Status: corev1.PodStatus{
						Phase: "COMPLETED",
					},
				},
			},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "batch",
					Version: "v1",
					Kind:    "Job",
				},
			},
			apiResourceLists: []*metav1.APIResourceList{
				{
					GroupVersion: "batch/v1",
					APIResources: []metav1.APIResource{
						{
							SingularName: "job",
							Kind:         "Job",
							Group:        "batch",
						},
					},
				},
			},
			args: []string{"--for", "job/test-job"},
			wantOut: `NAME          STATUS      AGE
valid-pod-1   RUNNING     60m
valid-pod-2   COMPLETED   60m
`,
		}, {
			name: "no valid pods for batch/job type in current namespace",
			job: []runtime.Object{
				&batchv1.Job{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-job",
						Namespace: metav1.NamespaceDefault,
						Labels: map[string]string{
							batchv1.JobNameLabel: "test-job",
						},
					},
				},
			},
			objs: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							batchv1.JobNameLabel: "sample-job",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "batch/v1",
								Kind:       "Job",
								Name:       "sample-job",
							},
						},
					},
					Status: corev1.PodStatus{
						Phase: "RUNNING",
					},
				}, &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-2",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							batchv1.JobNameLabel: "sample-job",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "batch/v1",
								Kind:       "Job",
								Name:       "sample-job",
							},
						},
					},
					Status: corev1.PodStatus{
						Phase: "COMPLETED",
					},
				},
			},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "batch",
					Version: "v1",
					Kind:    "Job",
				},
			},
			apiResourceLists: []*metav1.APIResourceList{
				{
					GroupVersion: "batch/v1",
					APIResources: []metav1.APIResource{
						{
							SingularName: "job",
							Kind:         "Job",
							Group:        "batch",
						},
					},
				},
			},
			args:    []string{"--for", "job/test-job"},
			wantOut: "",
			wantOutErr: `No resources found in default namespace.
`,
		}, {
			name: "no valid pods for batch/job type in all namespaces",
			job: []runtime.Object{
				&batchv1.Job{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-job",
						Namespace: metav1.NamespaceDefault,
						Labels: map[string]string{
							batchv1.JobNameLabel: "test-job",
						},
					},
				},
			},
			objs: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							batchv1.JobNameLabel: "sample-job",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "batch/v1",
								Kind:       "Job",
								Name:       "sample-job",
							},
						},
					},
					Status: corev1.PodStatus{
						Phase: "RUNNING",
					},
				}, &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-2",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							batchv1.JobNameLabel: "sample-job",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "batch/v1",
								Kind:       "Job",
								Name:       "sample-job",
							},
						},
					},
					Status: corev1.PodStatus{
						Phase: "COMPLETED",
					},
				},
			},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "batch",
					Version: "v1",
					Kind:    "Job",
				},
			},
			apiResourceLists: []*metav1.APIResourceList{
				{
					GroupVersion: "batch/v1",
					APIResources: []metav1.APIResource{
						{
							SingularName: "job",
							Kind:         "Job",
							Group:        "batch",
						},
					},
				},
			},
			args:    []string{"--for", "job/test-job", "-A"},
			wantOut: "",
			wantOutErr: `No resources found.
`,
		}, {
			name: "valid pods for batch/job type in all namespaces",
			job: []runtime.Object{
				&batchv1.Job{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "sample-job",
						Namespace: metav1.NamespaceDefault,
						Labels: map[string]string{
							batchv1.JobNameLabel: "sample-job",
						},
					},
				},
			},
			objs: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: "dev-team-a",
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							batchv1.JobNameLabel: "sample-job",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "batch/v1",
								Kind:       "Job",
								Name:       "sample-job",
							},
						},
					},
					Status: corev1.PodStatus{
						Phase: "RUNNING",
					},
				}, &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-2",
						Namespace: "dev-team-b",
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							batchv1.JobNameLabel: "sample-job",
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "batch/v1",
								Kind:       "Job",
								Name:       "sample-job",
							},
						},
					},
					Status: corev1.PodStatus{
						Phase: "COMPLETED",
					},
				},
			},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "batch",
					Version: "v1",
					Kind:    "Job",
				},
			},
			apiResourceLists: []*metav1.APIResourceList{
				{
					GroupVersion: "batch/v1",
					APIResources: []metav1.APIResource{
						{
							SingularName: "job",
							Kind:         "Job",
							Group:        "batch",
						},
					},
				},
			},
			args: []string{"--for", "job/sample-job", "-A"},
			wantOut: `NAMESPACE    NAME          STATUS      AGE
dev-team-a   valid-pod-1   RUNNING     60m
dev-team-b   valid-pod-2   COMPLETED   60m
`,
		}, {
			name: "list pods for kubeflow.org/PyTorchJob type",
			job: []runtime.Object{
				&kftraining.PyTorchJob{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-job",
						Namespace: metav1.NamespaceDefault,
						Labels: map[string]string{
							kftraining.OperatorNameLabel: "pytorchjob-controller",
							kftraining.JobNameLabel:      "test-job",
						},
					},
				},
			},
			objs: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							kftraining.OperatorNameLabel: "pytorchjob-controller",
							kftraining.JobNameLabel:      "test-job",
						},
					},
					Status: corev1.PodStatus{
						Phase: "RUNNING",
					},
				}, &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-2",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							kftraining.OperatorNameLabel: "pytorchjob-controller",
							kftraining.JobNameLabel:      "sample-job",
						},
					},
					Status: corev1.PodStatus{
						Phase: "COMPLETED",
					},
				},
			},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "kubeflow.org",
					Version: "v1",
					Kind:    "PyTorchJob",
				},
			},
			apiResourceLists: []*metav1.APIResourceList{
				{
					GroupVersion: "kubeflow.org/v1",
					APIResources: []metav1.APIResource{
						{
							SingularName: "pytorchjob",
							Kind:         "PyTorchJob",
							Group:        "kubeflow.org",
						},
					},
				},
			},
			args: []string{"--for", "pytorchjob/test-job"},
			wantOut: `NAME          STATUS    AGE
valid-pod-1   RUNNING   60m
`,
		}, {
			name: "list pods for kubeflow.org/MXjob type",
			job: []runtime.Object{
				&kftraining.MXJob{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-job",
						Namespace: metav1.NamespaceDefault,
						Labels: map[string]string{
							kftraining.OperatorNameLabel: "mxjob-controller",
							kftraining.JobNameLabel:      "test-job",
						},
					},
				},
			},
			objs: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							kftraining.OperatorNameLabel: "mxjob-controller",
							kftraining.JobNameLabel:      "test-job",
						},
					},
					Status: corev1.PodStatus{
						Phase: "RUNNING",
					},
				}, &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-2",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							kftraining.OperatorNameLabel: "mxjob-controller",
							kftraining.JobNameLabel:      "sample-job",
						},
					},
					Status: corev1.PodStatus{
						Phase: "COMPLETED",
					},
				},
			},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "kubeflow.org",
					Version: "v1",
					Kind:    "MXJob",
				},
			},
			apiResourceLists: []*metav1.APIResourceList{
				{
					GroupVersion: "kubeflow.org/v1",
					APIResources: []metav1.APIResource{
						{
							SingularName: "mxjob",
							Kind:         "MXJob",
							Group:        "kubeflow.org",
						},
					},
				},
			},
			args: []string{"--for", "mxjob/test-job"},
			wantOut: `NAME          STATUS    AGE
valid-pod-1   RUNNING   60m
`,
		}, {
			name: "list pods for kubeflow.org/paddlejob type",
			job: []runtime.Object{
				&kftraining.PaddleJob{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-job",
						Namespace: metav1.NamespaceDefault,
						Labels: map[string]string{
							kftraining.OperatorNameLabel: "paddlejob-controller",
							kftraining.JobNameLabel:      "test-job",
						},
					},
				},
			},
			objs: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							kftraining.OperatorNameLabel: "paddlejob-controller",
							kftraining.JobNameLabel:      "test-job",
						},
					},
					Status: corev1.PodStatus{
						Phase: "RUNNING",
					},
				}, &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-2",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							kftraining.OperatorNameLabel: "paddlejob-controller",
							kftraining.JobNameLabel:      "sample-job",
						},
					},
					Status: corev1.PodStatus{
						Phase: "COMPLETED",
					},
				},
			},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "kubeflow.org",
					Version: "v1",
					Kind:    "PaddleJob",
				},
			},
			apiResourceLists: []*metav1.APIResourceList{
				{
					GroupVersion: "kubeflow.org/v1",
					APIResources: []metav1.APIResource{
						{
							SingularName: "paddlejob",
							Kind:         "PaddleJob",
							Group:        "kubeflow.org",
						},
					},
				},
			},
			args: []string{"--for", "paddlejob/test-job"},
			wantOut: `NAME          STATUS    AGE
valid-pod-1   RUNNING   60m
`,
		}, {
			name: "list pods for kubeflow.org/tfjob type",
			job: []runtime.Object{
				&kftraining.TFJob{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-job",
						Namespace: metav1.NamespaceDefault,
						Labels: map[string]string{
							kftraining.OperatorNameLabel: "tfjob-controller",
							kftraining.JobNameLabel:      "test-job",
						},
					},
				},
			},
			objs: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							kftraining.OperatorNameLabel: "tfjob-controller",
							kftraining.JobNameLabel:      "test-job",
						},
					},
					Status: corev1.PodStatus{
						Phase: "RUNNING",
					},
				}, &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-2",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							kftraining.OperatorNameLabel: "tfjob-controller",
							kftraining.JobNameLabel:      "sample-job",
						},
					},
					Status: corev1.PodStatus{
						Phase: "COMPLETED",
					},
				},
			},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "kubeflow.org",
					Version: "v1",
					Kind:    "TFJob",
				},
			},
			apiResourceLists: []*metav1.APIResourceList{
				{
					GroupVersion: "kubeflow.org/v1",
					APIResources: []metav1.APIResource{
						{
							SingularName: "tfjob",
							Kind:         "TFJob",
							Group:        "kubeflow.org",
						},
					},
				},
			},
			args: []string{"--for", "tfjob/test-job"},
			wantOut: `NAME          STATUS    AGE
valid-pod-1   RUNNING   60m
`,
		}, {
			name: "list pods for kubeflow.org/mpijob type",
			job: []runtime.Object{
				&kftraining.MPIJob{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-job",
						Namespace: metav1.NamespaceDefault,
						Labels: map[string]string{
							kftraining.OperatorNameLabel: "mpijob-controller",
							kftraining.JobNameLabel:      "test-job",
						},
					},
				},
			},
			objs: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							kftraining.OperatorNameLabel: "mpijob-controller",
							kftraining.JobNameLabel:      "test-job",
						},
					},
					Status: corev1.PodStatus{
						Phase: "RUNNING",
					},
				}, &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-2",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							kftraining.OperatorNameLabel: "mpijob-controller",
							kftraining.JobNameLabel:      "sample-job",
						},
					},
					Status: corev1.PodStatus{
						Phase: "COMPLETED",
					},
				},
			},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "kubeflow.org",
					Version: "v2beta1",
					Kind:    "MPIJob",
				},
			},
			apiResourceLists: []*metav1.APIResourceList{
				{
					GroupVersion: "kubeflow.org/v2beta1",
					APIResources: []metav1.APIResource{
						{
							SingularName: "mpijob",
							Kind:         "MPIJob",
							Group:        "kubeflow.org",
						},
					},
				},
			},
			args: []string{"--for", "mpijob/test-job"},
			wantOut: `NAME          STATUS    AGE
valid-pod-1   RUNNING   60m
`,
		}, {
			name: "list pods for kubeflow.org/xgboostjob type",
			job: []runtime.Object{
				&kftraining.XGBoostJob{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-job",
						Namespace: metav1.NamespaceDefault,
						Labels: map[string]string{
							kftraining.OperatorNameLabel: "xgboostjob-controller",
							kftraining.JobNameLabel:      "test-job",
						},
					},
				},
			},
			objs: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							kftraining.OperatorNameLabel: "xgboostjob-controller",
							kftraining.JobNameLabel:      "test-job",
						},
					},
					Status: corev1.PodStatus{
						Phase: "RUNNING",
					},
				}, &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-2",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							kftraining.OperatorNameLabel: "xgboostjob-controller",
							kftraining.JobNameLabel:      "sample-job",
						},
					},
					Status: corev1.PodStatus{
						Phase: "COMPLETED",
					},
				},
			},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "kubeflow.org",
					Version: "v1",
					Kind:    "XGBoostJob",
				},
			},
			apiResourceLists: []*metav1.APIResourceList{
				{
					GroupVersion: "kubeflow.org/v1",
					APIResources: []metav1.APIResource{
						{
							SingularName: "xgboostjob",
							Kind:         "XGBoostJob",
							Group:        "kubeflow.org",
						},
					},
				},
			},
			args: []string{"--for", "xgboostjob/test-job"},
			wantOut: `NAME          STATUS    AGE
valid-pod-1   RUNNING   60m
`,
		}, {
			name: "list pods for ray.io/rayjob type",
			job: []runtime.Object{
				&rayv1.RayJob{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-job",
						Namespace: metav1.NamespaceDefault,
						Labels: map[string]string{
							batchv1.JobNameLabel: "test-job",
						},
					},
				},
			},
			objs: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							batchv1.JobNameLabel: "test-job",
						},
					},
					Status: corev1.PodStatus{
						Phase: "RUNNING",
					},
				}, &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-2",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							batchv1.JobNameLabel: "invalid-job",
						},
					},
					Status: corev1.PodStatus{
						Phase: "COMPLETED",
					},
				},
			},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "ray.io",
					Version: "v1",
					Kind:    "RayJob",
				},
			},
			apiResourceLists: []*metav1.APIResourceList{
				{
					GroupVersion: "ray.io/v1",
					APIResources: []metav1.APIResource{
						{
							SingularName: "rayjob",
							Kind:         "RayJob",
							Group:        "ray.io",
						},
					},
				},
			},
			args: []string{"--for", "rayjob/test-job"},
			wantOut: `NAME          STATUS    AGE
valid-pod-1   RUNNING   60m
`,
		}, {
			name: "list pods for ray.io/raycluster type",
			job: []runtime.Object{
				&rayv1.RayCluster{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-cluster",
						Namespace: metav1.NamespaceDefault,
						Labels: map[string]string{
							rayutils.RayClusterLabelKey: "test-cluster",
						},
					},
				},
			},
			objs: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							rayutils.RayClusterLabelKey: "test-cluster",
						},
					},
					Status: corev1.PodStatus{
						Phase: "RUNNING",
					},
				}, &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-2",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							rayutils.RayClusterLabelKey: "invalid-cluster",
						},
					},
					Status: corev1.PodStatus{
						Phase: "COMPLETED",
					},
				},
			},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "ray.io",
					Version: "v1",
					Kind:    "RayCluster",
				},
			},
			apiResourceLists: []*metav1.APIResourceList{
				{
					GroupVersion: "ray.io/v1",
					APIResources: []metav1.APIResource{
						{
							SingularName: "raycluster",
							Kind:         "RayCluster",
							Group:        "ray.io",
						},
					},
				},
			},
			args: []string{"--for", "raycluster/test-cluster"},
			wantOut: `NAME          STATUS    AGE
valid-pod-1   RUNNING   60m
`,
		}, {
			name: "list pods for jobset.x-k8s.io/jobset type",
			job: []runtime.Object{
				&jobsetapi.JobSet{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-job",
						Namespace: metav1.NamespaceDefault,
						Labels: map[string]string{
							jobsetapi.JobSetNameKey: "test-job",
						},
					},
				},
			},
			objs: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							jobsetapi.JobSetNameKey: "test-job",
						},
					},
					Status: corev1.PodStatus{
						Phase: "RUNNING",
					},
				}, &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-2",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							jobsetapi.JobSetNameKey: "sample-job",
						},
					},
					Status: corev1.PodStatus{
						Phase: "COMPLETED",
					},
				},
			},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "jobset.x-k8s.io",
					Version: "v1alpha2",
					Kind:    "JobSet",
				},
			},
			apiResourceLists: []*metav1.APIResourceList{
				{
					GroupVersion: "jobset.x-k8s.io/v1alpha2",
					APIResources: []metav1.APIResource{
						{
							SingularName: "jobset",
							Kind:         "JobSet",
							Group:        "jobset.x-k8s.io",
						},
					},
				},
			},
			args: []string{"--for", "jobset/test-job"},
			wantOut: `NAME          STATUS    AGE
valid-pod-1   RUNNING   60m
`,
		}, {
			name: "list pods with api-group filter",
			job: []runtime.Object{
				&jobsetapi.JobSet{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-job",
						Namespace: metav1.NamespaceDefault,
						Labels: map[string]string{
							jobsetapi.JobSetNameKey: "test-job",
						},
					},
				},
			},
			objs: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							jobsetapi.JobSetNameKey: "test-job",
						},
					},
					Status: corev1.PodStatus{
						Phase: "RUNNING",
					},
				}, &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-2",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						Labels: map[string]string{
							jobsetapi.JobSetNameKey: "sample-job",
						},
					},
					Status: corev1.PodStatus{
						Phase: "COMPLETED",
					},
				},
			},
			mapperGVKs: []schema.GroupVersionKind{
				{
					Group:   "jobset.x-k8s.io",
					Version: "v1alpha2",
					Kind:    "JobSet",
				},
			},
			apiResourceLists: []*metav1.APIResourceList{
				{
					GroupVersion: "jobset.x-k8s.io/v1alpha2",
					APIResources: []metav1.APIResource{
						{
							SingularName: "jobset",
							Kind:         "JobSet",
							Group:        "jobset.x-k8s.io",
						},
					},
				},
			},
			args: []string{"--for", "jobset.jobset.x-k8s.io/test-job"},
			wantOut: `NAME          STATUS    AGE
valid-pod-1   RUNNING   60m
`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			streams, _, out, outErr := genericiooptions.NewTestIOStreams()

			mapper := func() *meta.DefaultRESTMapper {
				m := meta.NewDefaultRESTMapper([]schema.GroupVersion{})
				for _, gvk := range tc.mapperGVKs {
					m.Add(gvk, meta.RESTScopeNamespace)
				}
				return m
			}()

			tf := cmdtesting.NewTestClientGetter()
			tf.WithNamespace(metav1.NamespaceDefault)
			tf.WithRestMapper(mapper)

			if len(tc.job) != 0 {
				scheme := runtime.NewScheme()
				if err := corev1.AddToScheme(scheme); err != nil {
					t.Errorf("Unexpected error\n%s", err)
				}
				if err := batchv1.AddToScheme(scheme); err != nil {
					t.Errorf("Unexpected error\n%s", err)
				}
				if err := rayv1.AddToScheme(scheme); err != nil {
					t.Errorf("Unexpected error\n%s", err)
				}
				if err := kftraining.AddToScheme(scheme); err != nil {
					t.Errorf("Unexpected error\n%s", err)
				}
				if err := jobsetapi.AddToScheme(scheme); err != nil {
					t.Errorf("Unexpected error\n%s", err)
				}

				codec := serializer.NewCodecFactory(scheme).LegacyCodec(scheme.PrioritizedVersionsAllGroups()...)

				tf.UnstructuredClient = &restfake.RESTClient{
					NegotiatedSerializer: resource.UnstructuredPlusDefaultContentConfig().NegotiatedSerializer,
					Resp: &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewReader([]byte(runtime.EncodeOrDie(codec, tc.job[0])))),
					},
				}
			}

			clientset := k8sfake.NewSimpleClientset(tc.objs...)
			tf.K8sClientset = clientset
			tf.K8sClientset.Discovery().(*fakediscovery.FakeDiscovery).Resources = tc.apiResourceLists

			cmd := NewPodCmd(tf, streams)
			cmd.SetArgs(tc.args)

			gotErr := cmd.Execute()

			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}

			gotOut := out.String()
			if diff := cmp.Diff(tc.wantOut, gotOut); diff != "" {
				t.Errorf("Unexpected output (-want/+got)\n%s", diff)
			}

			gotOutErr := outErr.String()
			if diff := cmp.Diff(tc.wantOutErr, gotOutErr); diff != "" {
				t.Errorf("Unexpected output (-want/+got)\n%s", diff)
			}
		})
	}
}
