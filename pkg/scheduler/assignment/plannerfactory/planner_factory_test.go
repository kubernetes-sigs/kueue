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
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/assignment/native"
	"sigs.k8s.io/kueue/pkg/scheduler/assignment/was"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestNewPlanner(t *testing.T) {
	nativeType := reflect.TypeOf(native.NewPlanner(nil, nil, nil, nil))
	wasType := reflect.TypeOf(was.NewPlanner(nil, nil, nil))

	const (
		nativePlannerLog   = "Delegating to Kueue Native Planner"
		wasPlannerLog      = "Delegating to WAS Planner"
		fallbackPlannerLog = "When deep scheduler-library integration is disabled, all workloads are routed to the default planner, regardless of the planner requested"
	)

	cases := map[string]struct {
		deepIntegration bool
		cqPlanner       kueue.AssignmentPlannerName
		wantType        reflect.Type
		wantLogMsgs     []string
		wantFallbackKVs map[string]any
		wantErrSubstr   string
	}{
		"deep integration disabled with KubeSchedulerDeepIntegration falls back to native planner": {
			deepIntegration: false,
			cqPlanner:       kueue.KubeSchedulerDeepIntegration,
			wantType:        nativeType,
			wantLogMsgs:     []string{fallbackPlannerLog, nativePlannerLog},
			wantFallbackKVs: map[string]any{
				"featureGate":      features.SchedulerLibraryDeepIntegration,
				"requestedPlanner": kueue.KubeSchedulerDeepIntegration,
				"defaultPlanner":   kueue.KueueNative,
			},
		},
		"deep integration disabled with KueueNative returns native planner without fallback log": {
			deepIntegration: false,
			cqPlanner:       kueue.KueueNative,
			wantType:        nativeType,
			wantLogMsgs:     []string{nativePlannerLog},
		},
		"deep integration enabled with KueueNative returns native planner": {
			deepIntegration: true,
			cqPlanner:       kueue.KueueNative,
			wantType:        nativeType,
			wantLogMsgs:     []string{nativePlannerLog},
		},
		"deep integration enabled with KubeSchedulerDeepIntegration returns was planner": {
			deepIntegration: true,
			cqPlanner:       kueue.KubeSchedulerDeepIntegration,
			wantType:        wasType,
			wantLogMsgs:     []string{wasPlannerLog},
		},
		"deep integration enabled with unknown planner returns error": {
			deepIntegration: true,
			cqPlanner:       kueue.AssignmentPlannerName("UnknownPlanner"),
			wantErrSubstr:   "unknown assignment planner requested: UnknownPlanner",
		},
		"deep integration disabled with unknown planner returns error": {
			deepIntegration: false,
			cqPlanner:       kueue.AssignmentPlannerName("UnknownPlanner"),
			wantErrSubstr:   "unknown assignment planner requested: UnknownPlanner",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.SchedulerLibraryDeepIntegration, tc.deepIntegration)
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
			var gotFallbackKVs map[string]any
			for _, entry := range plannerLog.GetSink().(ktesting.Underlier).GetBuffer().Data() {
				gotLogMsgs = append(gotLogMsgs, entry.Message)
				if entry.Message == fallbackPlannerLog {
					gotFallbackKVs = make(map[string]any, len(entry.ParameterKVList)/2)
					for i := 0; i+1 < len(entry.ParameterKVList); i += 2 {
						if k, ok := entry.ParameterKVList[i].(string); ok {
							gotFallbackKVs[k] = entry.ParameterKVList[i+1]
						}
					}
				}
			}
			if diff := cmp.Diff(tc.wantLogMsgs, gotLogMsgs); diff != "" {
				t.Errorf("Unexpected NewPlanner logs (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantFallbackKVs, gotFallbackKVs); diff != "" {
				t.Errorf("Unexpected fallback log key-value pairs (-want,+got):\n%s", diff)
			}
		})
	}
}
