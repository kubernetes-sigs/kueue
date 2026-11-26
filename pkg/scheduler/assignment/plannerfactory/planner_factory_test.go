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
	"sigs.k8s.io/kueue/pkg/scheduler/assignment/native"
	"sigs.k8s.io/kueue/pkg/scheduler/assignment/was"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestNewPlanner(t *testing.T) {
	nativeType := reflect.TypeOf(native.NewPlanner(nil, nil, nil, nil))
	wasType := reflect.TypeOf(was.NewPlanner(nil, nil, nil))

	const usingPlannerLog = "Using Planner"

	cases := map[string]struct {
		cqPlanner     kueue.AssignmentPlannerName
		wantType      reflect.Type
		wantErrSubstr string
	}{
		"KueueNative returns native planner": {
			cqPlanner: kueue.KueueNative,
			wantType:  nativeType,
		},
		"KubeSchedulerDeepIntegration returns was planner": {
			cqPlanner: kueue.KubeSchedulerDeepIntegration,
			wantType:  wasType,
		},
		"unknown planner returns error": {
			cqPlanner:     kueue.AssignmentPlannerName("UnknownPlanner"),
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
			got, err := NewPlanner(plannerLog, &workload.Info{}, cq, nil, nil, nil)
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
