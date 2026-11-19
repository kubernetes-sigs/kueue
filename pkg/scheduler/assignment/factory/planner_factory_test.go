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

package factory

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"k8s.io/klog/v2/ktesting"
	ctrl "sigs.k8s.io/controller-runtime"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schedcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/assignment/native"
	"sigs.k8s.io/kueue/pkg/scheduler/assignment/was"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestNewPlanner(t *testing.T) {
	nativeType := reflect.TypeOf(native.NewPlanner(nil, nil, nil, nil))
	wasType := reflect.TypeOf(was.NewPlanner(nil, nil, nil))

	cases := map[string]struct {
		deepIntegration bool
		cqName          kueue.ClusterQueueReference
		cqStrategy      kueue.SchedulingSimulationStrategy
		omitCQ          bool
		wantType        reflect.Type
		wantLogMsg      string
		wantErrSubstr   string
	}{
		"deep integration disabled ignores KubernetesSchedulerLibrary": {
			deepIntegration: false,
			cqName:          "cq",
			cqStrategy:      kueue.KubernetesSchedulerLibrary,
			wantType:        nativeType,
			wantLogMsg:      "Delegating to Kueue Native Planner",
		},
		"deep integration enabled with KueueNative returns native planner": {
			deepIntegration: true,
			cqName:          "cq",
			cqStrategy:      kueue.KueueNative,
			wantType:        nativeType,
			wantLogMsg:      "Delegating to Kueue Native Planner",
		},
		"deep integration enabled with KubernetesSchedulerLibrary returns was planner": {
			deepIntegration: true,
			cqName:          "cq",
			cqStrategy:      kueue.KubernetesSchedulerLibrary,
			wantType:        wasType,
			wantLogMsg:      "Delegating to WAS Planner",
		},
		"deep integration enabled with missing ClusterQueue returns error": {
			deepIntegration: true,
			cqName:          "missing-cq",
			omitCQ:          true,
			wantErrSubstr:   "ClusterQueue(missing-cq) not found",
		},
		"deep integration enabled with unknown strategy returns error": {
			deepIntegration: true,
			cqName:          "cq",
			cqStrategy:      kueue.SchedulingSimulationStrategy("UnknownStrategy"),
			wantErrSubstr:   "unknown scheduling simulation strategy: UnknownStrategy",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.SchedulerLibraryDeepIntegration, tc.deepIntegration)
			log := ktesting.NewLogger(t, ktesting.NewConfig(ktesting.BufferLogs(true), ktesting.Verbosity(3)))
			ctx := ctrl.LoggerInto(t.Context(), log)
			cache := schedcache.New(utiltesting.NewFakeClient())

			if !tc.omitCQ {
				cq := utiltestingapi.MakeClusterQueue(string(tc.cqName)).Obj()
				if err := cache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("AddClusterQueue failed: %v", err)
				}
			}
			snap, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("Snapshot failed: %v", err)
			}
			if !tc.omitCQ {
				snap.ClusterQueue(tc.cqName).SchedulingSimulationStrategy = tc.cqStrategy
			}

			wlObj := utiltestingapi.MakeWorkload("wl", "default").Obj()
			wl := &workload.Info{Obj: wlObj, ClusterQueue: tc.cqName}
			got, err := NewPlanner(log, wl, snap, nil, nil)
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
			var gotLogMsg, gotWL string
			for _, entry := range log.GetSink().(ktesting.Underlier).GetBuffer().Data() {
				if entry.Message == tc.wantLogMsg {
					gotLogMsg = entry.Message
					for i := 0; i+1 < len(entry.WithKVList); i += 2 {
						if entry.WithKVList[i] == "workload" {
							gotWL = fmt.Sprint(entry.WithKVList[i+1])
						}
					}
				}
			}
			if gotLogMsg != tc.wantLogMsg {
				t.Errorf("expected delegation log %q, got %q", tc.wantLogMsg, gotLogMsg)
			}
			if gotWL != "default/wl" {
				t.Errorf("expected delegation log workload %q, got %q", "default/wl", gotWL)
			}
		})
	}
}
