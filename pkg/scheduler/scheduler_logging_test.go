/*
Copyright The Kubernetes Authors.

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

package scheduler

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestScheduleLogging(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	cases := make(map[string]scheduleTestCase)
	for name, tc := range map[string]struct {
		featureGates map[featuregate.Feature]bool
		wantLevel    int
	}{
		"default gate preserves verbosity 2":  {wantLevel: 2},
		"disabled gate preserves verbosity 2": {featureGates: map[featuregate.Feature]bool{features.LogLevelForSchedulerMainLoop: false}, wantLevel: 2},
		"enabled gate uses verbosity 3":       {featureGates: map[featuregate.Feature]bool{features.LogLevelForSchedulerMainLoop: true}, wantLevel: 3},
	} {
		wantLogs := make(map[string]int)
		for _, message := range []string{
			"Scheduling cycle starts", "Scheduling cycle complete", "Obtained heads",
			"Snapshot taken", "Nomination done", "Workload processing done",
			"Attempting to schedule workload", "Workload re-queued",
		} {
			wantLogs[message] = tc.wantLevel
		}
		cases[name] = scheduleTestCase{
			featureGates: tc.featureGates,
			workloads:    []kueue.Workload{*utiltestingapi.MakeWorkload("pending", "default").Queue("lq").Request(corev1.ResourceCPU, "2").Obj()},
			wantWorkloads: []kueue.Workload{*utiltestingapi.MakeWorkload("pending", "default").Queue("lq").Request(corev1.ResourceCPU, "2").
				Condition(metav1.Condition{
					Type: kueue.WorkloadQuotaReserved, Status: metav1.ConditionFalse,
					Reason:             kueue.WorkloadQuotaReservedReasonExceedsMaxQuota,
					Message:            "couldn't assign flavors to pod set main: insufficient quota for cpu in flavor default, previously considered podsets requests (0) + current podset request (2) > maximum capacity (1)",
					LastTransitionTime: metav1.NewTime(now),
				}).
				Condition(metav1.Condition{
					Type: kueue.WorkloadAdmitted, Status: metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation", LastTransitionTime: metav1.NewTime(now),
				}).
				ResourceRequests(kueue.PodSetRequest{Name: "main", Resources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}}).Obj()},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{"cq": {"default/pending"}},
			wantSchedulerLogs:    wantLogs,
		}
	}
	runScheduleTestCases(t, scheduleTestConfig{
		queues:          []kueue.LocalQueue{*utiltestingapi.MakeLocalQueue("lq", "default").ClusterQueue("cq").Obj()},
		clusterQueues:   []kueue.ClusterQueue{*utiltestingapi.MakeClusterQueue("cq").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj()).Obj()},
		resourceFlavors: []*kueue.ResourceFlavor{utiltestingapi.MakeResourceFlavor("default").Obj()},
		fakeClock:       testingclock.NewFakeClock(now),
	}, cases)
}
