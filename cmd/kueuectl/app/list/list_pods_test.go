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
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/client-go/kubernetes/fake"
)

func TestPodOptions_listPods(t *testing.T) {
	testStartTime := time.Now()

	type fields struct {
		PrintFlags             *genericclioptions.PrintFlags
		AllNamespaces          bool
		Namespace              string
		LabelSelector          string
		FieldSelector          string
		UserSpecifiedForObject string
		ForObject              objectRef
	}
	tests := []struct {
		name    string
		pods    []runtime.Object
		fields  fields
		wantOut []string
		wantErr error
	}{
		{
			name: "list pods for valid batch/job type",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: "default",
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
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
						Namespace: "default",
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
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
			fields: fields{
				Namespace:              "default",
				UserSpecifiedForObject: "job/test-job",
			},
			wantOut: []string{
				"NAME          STATUS      AGE",
				"valid-pod-1   RUNNING     60m",
				"valid-pod-2   COMPLETED   60m",
				"",
			},
		}, {
			name: "no valid pods for batch/job type",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: "default",
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
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
						Namespace: "default",
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
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
			fields: fields{
				Namespace:              "default",
				UserSpecifiedForObject: "job/test-job",
			},
			wantOut: []string{
				"",
			},
		}, {
			name: "list pods for kubeflow.org/PyTorchJob type",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: "default",
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "kubeflow.org/v1",
								Kind:       "PyTorchJob",
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
						Namespace: "default",
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
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
			fields: fields{
				Namespace:              "default",
				UserSpecifiedForObject: "pytorchjob/test-job",
			},
			wantOut: []string{
				"NAME          STATUS    AGE",
				"valid-pod-1   RUNNING   60m",
				"",
			},
		}, {
			name: "list pods for kubeflow.org/MXjob type",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: "default",
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "kubeflow.org/v1",
								Kind:       "MXJob",
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
						Namespace: "default",
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
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
			fields: fields{
				Namespace:              "default",
				UserSpecifiedForObject: "mxjob/test-job",
			},
			wantOut: []string{
				"NAME          STATUS    AGE",
				"valid-pod-1   RUNNING   60m",
				"",
			},
		}, {
			name: "list pods for kubeflow.org/paddlejob type",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: "default",
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "kubeflow.org/v1",
								Kind:       "PaddleJob",
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
						Namespace: "default",
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
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
			fields: fields{
				Namespace:              "default",
				UserSpecifiedForObject: "paddlejob/test-job",
			},
			wantOut: []string{
				"NAME          STATUS    AGE",
				"valid-pod-1   RUNNING   60m",
				"",
			},
		}, {
			name: "list pods for kubeflow.org/tfjob type",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: "default",
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "kubeflow.org/v1",
								Kind:       "TFJob",
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
						Namespace: "default",
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
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
			fields: fields{
				Namespace:              "default",
				UserSpecifiedForObject: "tfjob/test-job",
			},
			wantOut: []string{
				"NAME          STATUS    AGE",
				"valid-pod-1   RUNNING   60m",
				"",
			},
		}, {
			name: "list pods for kubeflow.org/mpijob type",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: "default",
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "kubeflow.org/v1",
								Kind:       "MPIJob",
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
						Namespace: "default",
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
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
			fields: fields{
				Namespace:              "default",
				UserSpecifiedForObject: "mpijob/test-job",
			},
			wantOut: []string{
				"NAME          STATUS    AGE",
				"valid-pod-1   RUNNING   60m",
				"",
			},
		}, {
			name: "list pods for kubeflow.org/xgboostjob type",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: "default",
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "kubeflow.org/v1",
								Kind:       "XGBoostJob",
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
						Namespace: "default",
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
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
			fields: fields{
				Namespace:              "default",
				UserSpecifiedForObject: "xgboostjob/test-job",
			},
			wantOut: []string{
				"NAME          STATUS    AGE",
				"valid-pod-1   RUNNING   60m",
				"",
			},
		}, {
			name: "list pods for ray.io/rayjob type",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: "default",
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "ray.io/v1",
								Kind:       "RayJob",
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
						Namespace: "default",
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
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
			fields: fields{
				Namespace:              "default",
				UserSpecifiedForObject: "rayjob/test-job",
			},
			wantOut: []string{
				"NAME          STATUS    AGE",
				"valid-pod-1   RUNNING   60m",
				"",
			},
		}, {
			name: "list pods for ray.io/raycluster type",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: "default",
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "ray.io/v1",
								Kind:       "RayCluster",
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
						Namespace: "default",
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
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
			fields: fields{
				Namespace:              "default",
				UserSpecifiedForObject: "raycluster/test-job",
			},
			wantOut: []string{
				"NAME          STATUS    AGE",
				"valid-pod-1   RUNNING   60m",
				"",
			},
		}, {
			name: "list pods for jobset.x-k8s.io/jobset type",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: "default",
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "jobset.x-k8s.io/v1alpha2",
								Kind:       "JobSet",
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
						Namespace: "default",
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
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
			fields: fields{
				Namespace:              "default",
				UserSpecifiedForObject: "jobset/test-job",
			},
			wantOut: []string{
				"NAME          STATUS    AGE",
				"valid-pod-1   RUNNING   60m",
				"",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClientset := fake.NewSimpleClientset(tt.pods...)
			s, _, out, _ := genericiooptions.NewTestIOStreams()

			o := &PodOptions{
				PrintFlags:             tt.fields.PrintFlags,
				AllNamespaces:          tt.fields.AllNamespaces,
				Namespace:              tt.fields.Namespace,
				LabelSelector:          tt.fields.LabelSelector,
				FieldSelector:          tt.fields.FieldSelector,
				UserSpecifiedForObject: tt.fields.UserSpecifiedForObject,
				Clientset:              fakeClientset,
				PrintObj:               printPodTable,
				IOStreams:              s,
			}

			gotErr := o.parseForObject()
			gotErr = o.listPods(context.TODO())
			if diff := cmp.Diff(tt.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}

			gotOut := strings.Split(out.String(), "\n")

			if diff := cmp.Diff(tt.wantOut, gotOut); diff != "" {
				t.Errorf("Unexpected output (-want/+got)\n%s", diff)
			}
		})
	}
}
