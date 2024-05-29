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
	"testing"
	"time"

	cmdtesting "sigs.k8s.io/kueue/cmd/kueuectl/app/testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestPodCmd(t *testing.T) {
	testStartTime := time.Now()

	tests := []struct {
		name       string
		namespace  string
		pods       []runtime.Object
		args       []string
		wantOut    string
		wantOutErr string
		wantErr    error
	}{
		{
			name: "list pods for valid batch/job type",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: metav1.NamespaceDefault,
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
						Namespace: metav1.NamespaceDefault,
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
			args: []string{"--for", "job/test-job"},
			wantOut: `NAME          STATUS      AGE
valid-pod-1   RUNNING     60m
valid-pod-2   COMPLETED   60m
`,
		}, {
			name: "no valid pods for batch/job type in current namespace",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: metav1.NamespaceDefault,
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
						Namespace: metav1.NamespaceDefault,
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
			args:    []string{"--for", "job/test-job"},
			wantOut: "",
			wantOutErr: `No resources found in default namespace.
`,
		}, {
			name: "no valid pods for batch/job type in all namespaces",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: metav1.NamespaceDefault,
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
						Namespace: metav1.NamespaceDefault,
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
			args:    []string{"--for", "job/test-job", "-A"},
			wantOut: "",
			wantOutErr: `No resources found.
`,
		}, {
			name: "valid pods for batch/job type in all namespaces",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: "dev-team-a",
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
						Namespace: "dev-team-b",
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
			args: []string{"--for", "job/sample-job", "-A"},
			wantOut: `NAME          STATUS      AGE
valid-pod-1   RUNNING     60m
valid-pod-2   COMPLETED   60m
`,
		}, {
			name: "list pods for kubeflow.org/PyTorchJob type",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: metav1.NamespaceDefault,
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
						Namespace: metav1.NamespaceDefault,
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
			args: []string{"--for", "pytorchjob/test-job"},
			wantOut: `NAME          STATUS    AGE
valid-pod-1   RUNNING   60m
`,
		}, {
			name: "list pods for kubeflow.org/MXjob type",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: metav1.NamespaceDefault,
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
						Namespace: metav1.NamespaceDefault,
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
			args: []string{"--for", "mxjob/test-job"},
			wantOut: `NAME          STATUS    AGE
valid-pod-1   RUNNING   60m
`,
		}, {
			name: "list pods for kubeflow.org/paddlejob type",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: metav1.NamespaceDefault,
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
						Namespace: metav1.NamespaceDefault,
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
			args: []string{"--for", "paddlejob/test-job"},
			wantOut: `NAME          STATUS    AGE
valid-pod-1   RUNNING   60m
`,
		}, {
			name: "list pods for kubeflow.org/tfjob type",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: metav1.NamespaceDefault,
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
						Namespace: metav1.NamespaceDefault,
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
			args: []string{"--for", "tfjob/test-job"},
			wantOut: `NAME          STATUS    AGE
valid-pod-1   RUNNING   60m
`,
		}, {
			name: "list pods for kubeflow.org/mpijob type",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: metav1.NamespaceDefault,
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
						Namespace: metav1.NamespaceDefault,
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
			args: []string{"--for", "mpijob/test-job"},
			wantOut: `NAME          STATUS    AGE
valid-pod-1   RUNNING   60m
`,
		}, {
			name: "list pods for kubeflow.org/xgboostjob type",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: metav1.NamespaceDefault,
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
						Namespace: metav1.NamespaceDefault,
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
			args: []string{"--for", "xgboostjob/test-job"},
			wantOut: `NAME          STATUS    AGE
valid-pod-1   RUNNING   60m
`,
		}, {
			name: "list pods for ray.io/rayjob type",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: metav1.NamespaceDefault,
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
						Namespace: metav1.NamespaceDefault,
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
			args: []string{"--for", "rayjob/test-job"},
			wantOut: `NAME          STATUS    AGE
valid-pod-1   RUNNING   60m
`,
		}, {
			name: "list pods for ray.io/raycluster type",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: metav1.NamespaceDefault,
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
						Namespace: metav1.NamespaceDefault,
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
			args: []string{"--for", "raycluster/test-job"},
			wantOut: `NAME          STATUS    AGE
valid-pod-1   RUNNING   60m
`,
		}, {
			name: "list pods for jobset.x-k8s.io/jobset type",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "x-k8s.io/v1alpha2",
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
						Namespace: metav1.NamespaceDefault,
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
			args: []string{"--for", "jobset/test-job"},
			wantOut: `NAME          STATUS    AGE
valid-pod-1   RUNNING   60m
`,
		}, {
			name: "list pods with api-group filter",
			pods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "valid-pod-1",
						Namespace: metav1.NamespaceDefault,
						CreationTimestamp: metav1.Time{
							Time: testStartTime.Add(-time.Hour).Truncate(time.Second),
						},
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "x-k8s.io/v1alpha2",
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
						Namespace: metav1.NamespaceDefault,
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
			args: []string{"--for", "jobset.x-k8s.io/test-job"},
			wantOut: `NAME          STATUS    AGE
valid-pod-1   RUNNING   60m
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			streams, _, out, outErr := genericiooptions.NewTestIOStreams()

			tf := cmdtesting.NewTestClientGetter()
			tf.WithNamespace(metav1.NamespaceDefault)

			clientset := k8sfake.NewSimpleClientset(tt.pods...)
			tf.K8sClientset = clientset

			cmd := NewPodCmd(tf, streams)
			cmd.SetArgs(tt.args)

			gotErr := cmd.Execute()

			if diff := cmp.Diff(tt.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}

			gotOut := out.String()
			if diff := cmp.Diff(tt.wantOut, gotOut); diff != "" {
				t.Errorf("Unexpected output (-want/+got)\n%s", diff)
			}

			gotOutErr := outErr.String()
			if diff := cmp.Diff(tt.wantOutErr, gotOutErr); diff != "" {
				t.Errorf("Unexpected output (-want/+got)\n%s", diff)
			}
		})
	}
}
