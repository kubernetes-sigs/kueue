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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	testingclock "k8s.io/utils/clock/testing"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/client-go/clientset/versioned/fake"
	cmdtesting "sigs.k8s.io/kueue/cmd/kueuectl/app/testing"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestClusterQueueRun(t *testing.T) {
	testStartTime := time.Now()

	testCases := map[string]struct {
		ns         string
		objs       []runtime.Object
		args       []string
		wantOut    string
		wantOutErr string
		wantErr    error
	}{
		"should print active cluster queue list": {
			args: []string{"--active", "true"},
			objs: []runtime.Object{
				utiltesting.MakeClusterQueue("cq1").
					Creation(testStartTime.Add(-1*time.Hour).Truncate(time.Second)).
					Cohort("cohort1").
					PendingWorkloads(1).
					AdmittedWorkloads(2).
					Condition(v1beta1.ClusterQueueActive, metav1.ConditionTrue, "", "").
					Obj(),
				utiltesting.MakeClusterQueue("cq2").
					Creation(testStartTime.Add(-2*time.Hour).Truncate(time.Second)).
					Cohort("cohort2").
					PendingWorkloads(3).
					AdmittedWorkloads(4).
					Condition(v1beta1.ClusterQueueActive, metav1.ConditionFalse, "", "").
					Obj(),
			},
			wantOut: `NAME   COHORT    PENDING WORKLOADS   ADMITTED WORKLOADS   ACTIVE   AGE
cq1    cohort1   1                   2                    true     60m
`,
		},
		"should print inactive cluster queue list": {
			args: []string{"--active", "false"},
			objs: []runtime.Object{
				utiltesting.MakeClusterQueue("cq1").
					Creation(testStartTime.Add(-1*time.Hour).Truncate(time.Second)).
					Cohort("cohort1").
					PendingWorkloads(1).
					AdmittedWorkloads(2).
					Condition(v1beta1.ClusterQueueActive, metav1.ConditionTrue, "", "").
					Obj(),
				utiltesting.MakeClusterQueue("cq2").
					Creation(testStartTime.Add(-2*time.Hour).Truncate(time.Second)).
					Cohort("cohort2").
					PendingWorkloads(3).
					AdmittedWorkloads(4).
					Condition(v1beta1.ClusterQueueActive, metav1.ConditionFalse, "", "").
					Obj(),
			},
			wantOut: `NAME   COHORT    PENDING WORKLOADS   ADMITTED WORKLOADS   ACTIVE   AGE
cq2    cohort2   3                   4                    false    120m
`,
		},
		"should print cluster queue list with label selector": {
			args: []string{"--selector", "key=value1"},
			objs: []runtime.Object{
				utiltesting.MakeClusterQueue("cq1").
					Creation(testStartTime.Add(-1*time.Hour).Truncate(time.Second)).
					Cohort("cohort1").
					PendingWorkloads(1).
					AdmittedWorkloads(2).
					Condition(v1beta1.ClusterQueueActive, metav1.ConditionTrue, "", "").
					Label("key", "value1").
					Obj(),
				utiltesting.MakeClusterQueue("cq2").
					Creation(testStartTime.Add(-2*time.Hour).Truncate(time.Second)).
					Cohort("cohort2").
					PendingWorkloads(3).
					AdmittedWorkloads(4).
					Condition(v1beta1.ClusterQueueActive, metav1.ConditionFalse, "", "").
					Label("key", "value2").
					Obj(),
			},
			wantOut: `NAME   COHORT    PENDING WORKLOADS   ADMITTED WORKLOADS   ACTIVE   AGE
cq1    cohort1   1                   2                    true     60m
`,
		},
		"should print cluster queue list with label selector (short flag)": {
			args: []string{"-l", "key=value1"},
			objs: []runtime.Object{
				utiltesting.MakeClusterQueue("cq1").
					Creation(testStartTime.Add(-1*time.Hour).Truncate(time.Second)).
					Cohort("cohort1").
					PendingWorkloads(1).
					AdmittedWorkloads(2).
					Condition(v1beta1.ClusterQueueActive, metav1.ConditionTrue, "", "").
					Label("key", "value1").
					Obj(),
				utiltesting.MakeClusterQueue("cq2").
					Creation(testStartTime.Add(-2*time.Hour).Truncate(time.Second)).
					Cohort("cohort2").
					PendingWorkloads(3).
					AdmittedWorkloads(4).
					Condition(v1beta1.ClusterQueueActive, metav1.ConditionFalse, "", "").
					Label("key", "value2").
					Obj(),
			},
			wantOut: `NAME   COHORT    PENDING WORKLOADS   ADMITTED WORKLOADS   ACTIVE   AGE
cq1    cohort1   1                   2                    true     60m
`,
		},
		"should print not found error": {
			wantOutErr: "No resources found\n",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			streams, _, out, outErr := genericiooptions.NewTestIOStreams()

			tcg := cmdtesting.NewTestClientGetter().WithKueueClientset(fake.NewSimpleClientset(tc.objs...))

			cmd := NewClusterQueueCmd(tcg, streams, testingclock.NewFakeClock(testStartTime))
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
