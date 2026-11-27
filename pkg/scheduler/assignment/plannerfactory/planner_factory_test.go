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

package plannerfactory

import (
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/klog/v2/ktesting"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schedcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/scheduler/assignment/native"
	"sigs.k8s.io/kueue/pkg/scheduler/assignment/was"
	"sigs.k8s.io/kueue/pkg/workload"
)

type stubNodeFeasibilityEvaluator struct {
	simulator.NodeFeasibilityEvaluator
}

type stubSchedulerSimulator struct {
	simulator.SchedulerSimulator
}

func TestNewPlanner(t *testing.T) {
	nativeType := reflect.TypeOf(native.NewPlanner(nil, nil, nil, nil, nil))
	wasType := reflect.TypeOf(was.NewPlanner(nil, nil, nil))
	nativeEval := &stubNodeFeasibilityEvaluator{}
	schedulerSim := &stubSchedulerSimulator{}

	const usingPlannerLog = "Using Planner"

	cases := map[string]struct {
		cqPlanner     kueue.AssignmentPlannerName
		snapshot      *schedcache.Snapshot
		wantType      reflect.Type
		wantEvaluator simulator.NodeFeasibilityEvaluator
		wantErrSubstr string
	}{
		"KueueNative returns native planner with NativeNodeEvaluator": {
			cqPlanner:     kueue.KueueNative,
			snapshot:      &schedcache.Snapshot{NativeNodeEvaluator: nativeEval},
			wantType:      nativeType,
			wantEvaluator: nativeEval,
		},
		"KueueNative returns native planner with NativeNodeEvaluator even when SchedulerSimulator is set": {
			cqPlanner: kueue.KueueNative,
			snapshot: &schedcache.Snapshot{
				NativeNodeEvaluator: nativeEval,
				SchedulerSimulator:  schedulerSim,
			},
			wantType:      nativeType,
			wantEvaluator: nativeEval,
		},
		"KubeSchedulerShallowIntegration returns native planner with SchedulerSimulator": {
			cqPlanner: kueue.KubeSchedulerShallowIntegration,
			snapshot: &schedcache.Snapshot{
				NativeNodeEvaluator: nativeEval,
				SchedulerSimulator:  schedulerSim,
			},
			wantType:      nativeType,
			wantEvaluator: schedulerSim,
		},
		"KubeSchedulerDeepIntegration returns was planner": {
			cqPlanner: kueue.KubeSchedulerDeepIntegration,
			snapshot: &schedcache.Snapshot{
				NativeNodeEvaluator: nativeEval,
				SchedulerSimulator:  schedulerSim,
			},
			wantType: wasType,
		},
		"unknown planner returns error": {
			cqPlanner:     kueue.AssignmentPlannerName("UnknownPlanner"),
			snapshot:      &schedcache.Snapshot{},
			wantErrSubstr: "unknown assignment planner requested: UnknownPlanner",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cq := &schedcache.ClusterQueueSnapshot{
				Name:              "cq",
				AssignmentPlanner: tc.cqPlanner,
			}

			plannerLog := ktesting.NewLogger(t, ktesting.NewConfig(ktesting.BufferLogs(true), ktesting.Verbosity(3)))
			got, err := NewPlanner(plannerLog, &workload.Info{}, cq, tc.snapshot, nil, nil)
			if tc.wantErrSubstr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErrSubstr)
				}
				if !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Errorf("unexpected error %q, want substring %q", err.Error(), tc.wantErrSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewPlanner unexpected error: %v", err)
			}
			if gotType := reflect.TypeOf(got); gotType != tc.wantType {
				t.Errorf("NewPlanner returned %v, want %v", gotType, tc.wantType)
			}
			if tc.wantEvaluator != nil {
				gotEval := reflect.ValueOf(got).Elem().FieldByName("nodeFeasibilityEvaluator")
				if !gotEval.IsValid() || gotEval.IsNil() {
					t.Fatalf("NewPlanner returned planner with nil nodeFeasibilityEvaluator, want %T", tc.wantEvaluator)
				}
				if gotPtr, wantPtr := gotEval.Elem().Pointer(), reflect.ValueOf(tc.wantEvaluator).Pointer(); gotPtr != wantPtr {
					t.Errorf("NewPlanner evaluator pointer = %#x (%v), want %#x (%T)", gotPtr, gotEval.Elem().Type(), wantPtr, tc.wantEvaluator)
				}
			}

			var gotLogMsgs []string
			var gotPlanner any
			for _, entry := range plannerLog.GetSink().(ktesting.Underlier).GetBuffer().Data() {
				gotLogMsgs = append(gotLogMsgs, entry.Message)
				if entry.Message == usingPlannerLog {
					for i := 0; i+1 < len(entry.ParameterKVList); i += 2 {
						if k, ok := entry.ParameterKVList[i].(string); ok && k == "planner" {
							gotPlanner = entry.ParameterKVList[i+1]
						}
					}
				}
			}
			if diff := cmp.Diff([]string{usingPlannerLog}, gotLogMsgs); diff != "" {
				t.Errorf("Unexpected NewPlanner logs (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.cqPlanner, gotPlanner); diff != "" {
				t.Errorf("Unexpected planner log value (-want,+got):\n%s", diff)
			}
		})
	}
}
