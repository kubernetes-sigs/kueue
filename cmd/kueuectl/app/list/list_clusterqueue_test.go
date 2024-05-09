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
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/client-go/clientset/versioned/fake"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestClusterQueueRun(t *testing.T) {
	testStartTime := time.Now()

	testCases := map[string]struct {
		o       *ClusterQueueOptions
		wantOut []string
		wantErr error
	}{
		"should print all cluster queue list": {
			o: &ClusterQueueOptions{
				PrintObj: printClusterQueueTable,
				Active:   "*",
				Client: fake.NewSimpleClientset(
					utiltesting.MakeClusterQueue("cq1").
						Condition(v1beta1.ClusterQueueActive, metav1.ConditionTrue, "", "").
						Creation(testStartTime.Add(-time.Hour).Truncate(time.Second)).
						Obj(),
					utiltesting.MakeClusterQueue("cq2").
						Condition(v1beta1.ClusterQueueActive, metav1.ConditionFalse, "", "").
						Creation(testStartTime.Add(-time.Hour).Truncate(time.Second)).
						Obj(),
					utiltesting.MakeClusterQueue("cq3").
						Condition(v1beta1.ClusterQueueActive, metav1.ConditionUnknown, "", "").
						Creation(testStartTime.Add(-time.Hour).Truncate(time.Second)).
						Obj(),
					utiltesting.MakeClusterQueue("cq4").
						Condition("other", metav1.ConditionTrue, "", "").
						Creation(testStartTime.Add(-time.Hour).Truncate(time.Second)).
						Obj(),
				).KueueV1beta1(),
			},
			wantOut: []string{
				"NAME   COHORT   PENDING WORKLOADS   ADMITTED WORKLOADS   ACTIVE   AGE",
				"cq1             0                   0                    true     60m",
				"cq2             0                   0                    false    60m",
				"cq3             0                   0                    false    60m",
				"cq4             0                   0                    false    60m\n",
			},
		},
		"should print all cluster queue list if active is not provided": {
			o: &ClusterQueueOptions{
				PrintObj: printClusterQueueTable,
				Client: fake.NewSimpleClientset(
					utiltesting.MakeClusterQueue("cq1").
						Condition(v1beta1.ClusterQueueActive, metav1.ConditionTrue, "", "").
						Cohort("cohort1").
						Creation(testStartTime.Add(-time.Hour).Truncate(time.Second)).
						Obj(),
					utiltesting.MakeClusterQueue("cq2").
						Condition(v1beta1.ClusterQueueActive, metav1.ConditionFalse, "", "").
						PendingWorkloads(1).
						Creation(testStartTime.Add(-time.Hour).Truncate(time.Second)).
						Obj(),
					utiltesting.MakeClusterQueue("cq3").
						Condition(v1beta1.ClusterQueueActive, metav1.ConditionUnknown, "", "").
						AdmittedWorkloads(1).
						Creation(testStartTime.Add(-time.Hour).Truncate(time.Second)).
						Obj(),
					utiltesting.MakeClusterQueue("cq4").
						Condition("other", metav1.ConditionTrue, "", "").
						Creation(testStartTime.Add(-time.Hour).Truncate(time.Second)).
						Obj(),
				).KueueV1beta1(),
			},
			wantOut: []string{
				"NAME   COHORT    PENDING WORKLOADS   ADMITTED WORKLOADS   ACTIVE   AGE",
				"cq1    cohort1   0                   0                    true     60m",
				"cq2              1                   0                    false    60m",
				"cq3              0                   1                    false    60m",
				"cq4              0                   0                    false    60m\n",
			},
		},
		"should print active cluster queue list": {
			o: &ClusterQueueOptions{
				PrintObj: printClusterQueueTable,
				Active:   "true",
				Client: fake.NewSimpleClientset(
					utiltesting.MakeClusterQueue("cq1").
						Condition(v1beta1.ClusterQueueActive, metav1.ConditionTrue, "", "").
						Creation(testStartTime.Add(-time.Hour).Truncate(time.Second)).
						Obj(),
					utiltesting.MakeClusterQueue("cq2").
						Condition(v1beta1.ClusterQueueActive, metav1.ConditionFalse, "", "").
						Creation(testStartTime.Add(-time.Hour).Truncate(time.Second)).
						Obj(),
					utiltesting.MakeClusterQueue("cq3").
						Condition(v1beta1.ClusterQueueActive, metav1.ConditionUnknown, "", "").
						Creation(testStartTime.Add(-time.Hour).Truncate(time.Second)).
						Obj(),
					utiltesting.MakeClusterQueue("cq4").
						Condition("other", metav1.ConditionTrue, "", "").
						Creation(testStartTime.Add(-time.Hour).Truncate(time.Second)).
						Obj(),
				).KueueV1beta1(),
			},
			wantOut: []string{
				"NAME   COHORT   PENDING WORKLOADS   ADMITTED WORKLOADS   ACTIVE   AGE",
				"cq1             0                   0                    true     60m\n",
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			s, _, out, _ := genericiooptions.NewTestIOStreams()
			tc.o.IOStreams = s

			gotErr := tc.o.Run()
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}

			gotOut := out.String()
			joinedWantOut := strings.Join(tc.wantOut, "\n")

			if diff := cmp.Diff(joinedWantOut, gotOut); diff != "" {
				t.Errorf("Unexpected output (-want/+got)\n%s", diff)
			}
		})
	}
}
