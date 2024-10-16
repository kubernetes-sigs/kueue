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

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	testingclock "k8s.io/utils/clock/testing"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/client-go/clientset/versioned/fake"
	cmdtesting "sigs.k8s.io/kueue/cmd/kueuectl/app/testing"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestListCmd(t *testing.T) {
	testStartTime := time.Now()

	testCases := map[string]struct {
		ns         string
		objs       []runtime.Object
		args       []string
		wantOut    string
		wantOutErr string
		wantErr    error
	}{
		"should print local queue list with all namespaces": {
			args: []string{"localqueue", "--all-namespaces"},
			objs: []runtime.Object{
				utiltesting.MakeLocalQueue("lq1", "ns1").
					ClusterQueue("cq1").
					PendingWorkloads(1).
					AdmittedWorkloads(1).
					Creation(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeLocalQueue("lq2", "ns2").
					ClusterQueue("cq2").
					PendingWorkloads(2).
					AdmittedWorkloads(2).
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Obj(),
			},
			wantOut: `NAMESPACE   NAME   CLUSTERQUEUE   PENDING WORKLOADS   ADMITTED WORKLOADS   AGE
ns1         lq1    cq1            1                   1                    60m
ns2         lq2    cq2            2                   2                    120m
`,
		},
		"should print local queue list with all namespaces (short command and flag)": {
			args: []string{"lq", "-A"},
			objs: []runtime.Object{
				utiltesting.MakeLocalQueue("lq1", "ns1").
					ClusterQueue("cq1").
					PendingWorkloads(1).
					AdmittedWorkloads(1).
					Creation(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeLocalQueue("lq2", "ns2").
					ClusterQueue("cq2").
					PendingWorkloads(2).
					AdmittedWorkloads(2).
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Obj(),
			},
			wantOut: `NAMESPACE   NAME   CLUSTERQUEUE   PENDING WORKLOADS   ADMITTED WORKLOADS   AGE
ns1         lq1    cq1            1                   1                    60m
ns2         lq2    cq2            2                   2                    120m
`,
		},
		"should print cluster queue list": {
			args: []string{"clusterqueue"},
			objs: []runtime.Object{
				utiltesting.MakeClusterQueue("cq1").
					Condition(v1beta1.ClusterQueueActive, metav1.ConditionTrue, "", "").
					Cohort("cohort1").
					Creation(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeClusterQueue("cq2").
					Condition(v1beta1.ClusterQueueActive, metav1.ConditionFalse, "", "").
					Cohort("cohort2").
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Obj(),
			},
			wantOut: `NAME   COHORT    PENDING WORKLOADS   ADMITTED WORKLOADS   ACTIVE   AGE
cq1    cohort1   0                   0                    true     60m
cq2    cohort2   0                   0                    false    120m
`,
		},
		"should print workload list with all namespaces": {
			args: []string{"workload", "--all-namespaces"},
			objs: []runtime.Object{
				utiltesting.MakeWorkload("wl1", "ns1").
					OwnerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "j1", "test-uid").
					Queue("lq1").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq1").Obj()).
					Creation(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeWorkload("wl2", "ns2").
					OwnerReference(rayv1.GroupVersion.WithKind("RayJob"), "j2", "test-uid").
					Queue("lq2").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq2").Obj()).
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Obj(),
			},
			wantOut: `NAMESPACE   NAME   JOB TYPE   JOB NAME   LOCALQUEUE   CLUSTERQUEUE   STATUS    POSITION IN QUEUE   EXEC TIME   AGE
ns1         wl1               j1         lq1          cq1            PENDING                                   60m
ns2         wl2               j2         lq2          cq2            PENDING                                   120m
`,
		},
		"should print workload list with all namespaces (short command and flag)": {
			args: []string{"wl", "-A"},
			objs: []runtime.Object{
				utiltesting.MakeWorkload("wl1", "ns1").
					OwnerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "j1", "test-uid").
					Queue("lq1").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq1").Obj()).
					Creation(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeWorkload("wl2", "ns2").
					OwnerReference(rayv1.GroupVersion.WithKind("RayJob"), "j2", "test-uid").
					Queue("lq2").
					Active(true).
					Admission(utiltesting.MakeAdmission("cq2").Obj()).
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Obj(),
			},
			wantOut: `NAMESPACE   NAME   JOB TYPE   JOB NAME   LOCALQUEUE   CLUSTERQUEUE   STATUS    POSITION IN QUEUE   EXEC TIME   AGE
ns1         wl1               j1         lq1          cq1            PENDING                                   60m
ns2         wl2               j2         lq2          cq2            PENDING                                   120m
`,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			streams, _, out, outErr := genericiooptions.NewTestIOStreams()

			tcg := cmdtesting.NewTestClientGetter().WithKueueClientset(fake.NewSimpleClientset(tc.objs...))
			if len(tc.ns) > 0 {
				tcg.WithNamespace(tc.ns)
			}

			fc := testingclock.NewFakeClock(testStartTime)

			cmd := NewListCmd(tcg, streams, fc)
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
