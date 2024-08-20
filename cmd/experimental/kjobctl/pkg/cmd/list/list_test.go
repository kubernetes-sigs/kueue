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
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	rayfake "github.com/ray-project/kuberay/ray-operator/pkg/client/clientset/versioned/fake"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	testingclock "k8s.io/utils/clock/testing"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
	cmdtesting "sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/cmd/testing"
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/pkg/testing/wrappers"
)

func TestListCmd(t *testing.T) {
	testStartTime := time.Now()

	testCases := map[string]struct {
		ns         string
		k8sObjs    []runtime.Object
		rayObjs    []runtime.Object
		args       []string
		wantOut    string
		wantOutErr string
		wantErr    error
	}{
		"should print job list with all namespaces": {
			args: []string{"job", "--all-namespaces"},
			k8sObjs: []runtime.Object{
				wrappers.MakeJob("j1", "ns1").
					Profile("profile1").
					Mode(v1alpha1.JobMode).
					LocalQueue("lq1").
					Completions(3).
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					CompletionTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Succeeded(3).
					Obj(),
				wrappers.MakeJob("j2", "ns2").
					Profile("profile2").
					Mode(v1alpha1.JobMode).
					LocalQueue("lq1").
					Completions(3).
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					CompletionTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Succeeded(3).
					Obj(),
			},
			wantOut: `NAMESPACE   NAME   PROFILE    LOCAL QUEUE   COMPLETIONS   DURATION   AGE
ns1         j1     profile1   lq1           3/3           60m        60m
ns2         j2     profile2   lq1           3/3           60m        60m
`,
		},
		"should print slurm job list with all namespaces": {
			args: []string{"slurm", "--all-namespaces"},
			k8sObjs: []runtime.Object{
				wrappers.MakeJob("j1", "ns1").
					Profile("profile1").
					Mode(v1alpha1.SlurmMode).
					LocalQueue("lq1").
					Completions(3).
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					CompletionTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Succeeded(3).
					Obj(),
				wrappers.MakeJob("j2", "ns2").
					Profile("profile2").
					Mode(v1alpha1.SlurmMode).
					LocalQueue("lq1").
					Completions(3).
					CreationTimestamp(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					StartTime(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					CompletionTime(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Succeeded(3).
					Obj(),
			},
			wantOut: `NAMESPACE   NAME   PROFILE    LOCAL QUEUE   COMPLETIONS   DURATION   AGE
ns1         j1     profile1   lq1           3/3           60m        60m
ns2         j2     profile2   lq1           3/3           60m        60m
`,
		},
		"should print rayjob list with all namespaces": {
			args: []string{"rayjob", "--all-namespaces"},
			rayObjs: []runtime.Object{
				wrappers.MakeRayJob("rj1", metav1.NamespaceDefault).
					Profile("profile1").
					LocalQueue("lq1").
					JobStatus(rayv1.JobStatusSucceeded).
					RayClusterName("rc1").
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					CreationTimestamp(testStartTime.Add(-2 * time.Hour)).
					StartTime(testStartTime.Add(-2 * time.Hour)).
					EndTime(testStartTime.Add(-1 * time.Hour)).
					Obj(),
				wrappers.MakeRayJob("rj2", metav1.NamespaceDefault).
					Profile("profile2").
					LocalQueue("lq2").
					JobStatus(rayv1.JobStatusSucceeded).
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					CreationTimestamp(testStartTime.Add(-2 * time.Hour)).
					StartTime(testStartTime.Add(-2 * time.Hour)).
					EndTime(testStartTime.Add(-1 * time.Hour)).
					Obj(),
			},
			wantOut: fmt.Sprintf(`NAMESPACE   NAME   PROFILE    LOCAL QUEUE   RAY CLUSTER NAME   JOB STATUS   DEPLOYMENT STATUS   START TIME            END TIME              AGE
default     rj1    profile1   lq1           rc1                SUCCEEDED    Complete            %s   %s   120m
default     rj2    profile2   lq2                              SUCCEEDED    Complete            %s   %s   120m
`,
				testStartTime.Add(-2*time.Hour).Format(time.DateTime),
				testStartTime.Add(-1*time.Hour).Format(time.DateTime),
				testStartTime.Add(-2*time.Hour).Format(time.DateTime),
				testStartTime.Add(-1*time.Hour).Format(time.DateTime),
			),
		},
		"should print raycluster list with all namespaces": {
			args: []string{"raycluster", "--all-namespaces"},
			rayObjs: []runtime.Object{
				wrappers.MakeRayCluster("rc1", metav1.NamespaceDefault).
					Profile("profile1").
					LocalQueue("lq1").
					DesiredWorkerReplicas(2).
					AvailableWorkerReplicas(3).
					DesiredCPU(resource.MustParse("5")).
					DesiredMemory(resource.MustParse("10Gi")).
					DesiredGPU(resource.MustParse("10")).
					State(rayv1.Ready).
					CreationTimestamp(testStartTime.Add(-2 * time.Hour)).
					Obj(),
				wrappers.MakeRayCluster("rc2", metav1.NamespaceDefault).
					Profile("profile2").
					LocalQueue("lq2").
					DesiredWorkerReplicas(2).
					AvailableWorkerReplicas(3).
					DesiredCPU(resource.MustParse("5")).
					DesiredMemory(resource.MustParse("10Gi")).
					DesiredGPU(resource.MustParse("10")).
					State(rayv1.Ready).
					CreationTimestamp(testStartTime.Add(-2 * time.Hour)).
					Obj(),
			},
			wantOut: `NAMESPACE   NAME   PROFILE    LOCAL QUEUE   DESIRED WORKERS   AVAILABLE WORKERS   CPUS   MEMORY   GPUS   STATUS   AGE
default     rc1    profile1   lq1           2                 3                   5      10Gi     10     ready    120m
default     rc2    profile2   lq2           2                 3                   5      10Gi     10     ready    120m
`,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			streams, _, out, outErr := genericiooptions.NewTestIOStreams()

			tcg := cmdtesting.NewTestClientGetter()
			if len(tc.ns) > 0 {
				tcg.WithNamespace(tc.ns)
			}

			tcg.WithK8sClientset(k8sfake.NewSimpleClientset(tc.k8sObjs...))
			tcg.WithRayClientset(rayfake.NewSimpleClientset(tc.rayObjs...))

			cmd := NewListCmd(tcg, streams, testingclock.NewFakeClock(testStartTime))
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
