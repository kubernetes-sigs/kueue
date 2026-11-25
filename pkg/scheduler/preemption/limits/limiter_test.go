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

package limits

import (
	"cmp"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/go-logr/logr"
	gocmp "github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/policy"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestNewPreemptionLimiter(t *testing.T) {
	config := utiltestingalpha.MakePreemptionConfig("config").Label("tier", "gold").Obj()

	cases := map[string]struct {
		preemptionLimits []kueuealpha.PreemptionLimit
		want             []policy.PreemptionLimitReference
		wantErr          *selectorError
	}{
		"limits applying to the config are kept by name, the others are dropped": {
			preemptionLimits: []kueuealpha.PreemptionLimit{
				*utiltestingalpha.MakePreemptionLimit("any-config", kueuealpha.GlobalLimitAggregation).WindowSeconds(60).Obj(),
				*utiltestingalpha.MakePreemptionLimit("silver", kueuealpha.GlobalLimitAggregation).WindowSeconds(60).
					ConfigSelector(metav1.LabelSelector{MatchLabels: map[string]string{"tier": "silver"}}).Obj(),
			},
			want: []policy.PreemptionLimitReference{"any-config"},
		},
		"invalid limit is an error": {
			preemptionLimits: []kueuealpha.PreemptionLimit{
				*utiltestingalpha.MakePreemptionLimit("any-config", kueuealpha.GlobalLimitAggregation).WindowSeconds(60).Obj(),
				*utiltestingalpha.MakePreemptionLimit("invalid", kueuealpha.GlobalLimitAggregation).WindowSeconds(60).
					ConfigSelector(metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "tier", Operator: "invalid"}},
					}).Obj(),
			},
			wantErr: &selectorError{PreemptionLimit: "invalid", Selector: "configSelector"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			limiter, err := NewPreemptionLimiter(logr.Discard(), SnapshotForTest(time.Time{}, tc.preemptionLimits, nil), config, preemptexpectations.New())
			if diff := diffSelectorError(tc.wantErr, err); diff != "" {
				t.Fatalf("NewPreemptionLimiter() unexpected error (-want,+got):\n%s", diff)
			}
			if err != nil {
				return
			}
			got := slices.Sorted(maps.Keys(limiter.matchers))
			if diff := gocmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Unexpected limits (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestPreemptionLimiterConsumedLimits(t *testing.T) {
	preemptorCQ := &schdcache.ClusterQueueSnapshot{Name: "preempting-cq", Labels: map[string]string{"side": "preempting"}}
	targetCQ := &schdcache.ClusterQueueSnapshot{Name: "preempted-cq", Labels: map[string]string{"side": "preempted"}}
	preemptor := &workload.Info{Obj: utiltestingapi.MakeWorkload("preemptor", "ns").Obj(), ClusterQueue: "preempting-cq"}
	target := &workload.Info{Obj: utiltestingapi.MakeWorkload("target", "ns").UID("target").Obj(), ClusterQueue: "preempted-cq"}
	globalLimit := utiltestingalpha.MakePreemptionLimit("global", kueuealpha.GlobalLimitAggregation).WindowSeconds(60).Obj()
	preempteeWorkloadLimit := utiltestingalpha.MakePreemptionLimit("preemptee-workload", kueuealpha.ByPreempteeWorkloadLimitAggregation).WindowSeconds(60).Obj()

	cases := map[string]struct {
		preemptionLimits []kueuealpha.PreemptionLimit
		// target, when set, replaces the default target.
		target *workload.Info
		// unobservedEviction marks the eviction of the target as issued but not observed yet.
		unobservedEviction bool
		nilLimiter         bool
		want               policy.ConsumedLimits
	}{
		"no limits consume nothing": {},
		"limits matching the ClusterQueues are consumed together, the others are not": {
			preemptionLimits: []kueuealpha.PreemptionLimit{
				*globalLimit,
				*preempteeWorkloadLimit,
				*utiltestingalpha.MakePreemptionLimit("other-cqs", kueuealpha.ByPreempteeCQLimitAggregation).WindowSeconds(60).
					PreempteeClusterQueueSelector(&metav1.LabelSelector{MatchLabels: map[string]string{"side": "other"}}).Obj(),
			},
			want: policy.ConsumedLimits{
				"global":             policy.NewConsumedLimit(globalLimit, policy.GlobalAggregationKey),
				"preemptee-workload": policy.NewConsumedLimit(preempteeWorkloadLimit, "ns/target"),
			},
		},
		"evicted target consumes nothing": {
			preemptionLimits: []kueuealpha.PreemptionLimit{*globalLimit},
			target: &workload.Info{
				Obj:          utiltestingapi.MakeWorkload("target", "ns").UID("target").EvictedAt(time.Now()).Obj(),
				ClusterQueue: "preempted-cq",
			},
		},
		"target with an unobserved eviction consumes nothing": {
			preemptionLimits:   []kueuealpha.PreemptionLimit{*globalLimit},
			unobservedEviction: true,
		},
		"nil limiter consumes nothing": {
			preemptionLimits: []kueuealpha.PreemptionLimit{*globalLimit},
			nilLimiter:       true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			log := logr.Discard()
			preemptionExpectations := preemptexpectations.New()
			if tc.unobservedEviction {
				preemptionExpectations.ExpectUIDs(log, client.ObjectKeyFromObject(target.Obj), []types.UID{target.Obj.UID})
			}
			limiter, err := NewPreemptionLimiter(log, SnapshotForTest(time.Time{}, tc.preemptionLimits, nil),
				utiltestingalpha.MakePreemptionConfig("config").Obj(), preemptionExpectations)
			if err != nil {
				t.Fatalf("NewPreemptionLimiter() unexpected error: %v", err)
			}
			if tc.nilLimiter {
				limiter = nil
			}
			got := limiter.consumedLimits(preemptor, cmp.Or(tc.target, target), preemptorCQ, targetCQ)
			if diff := gocmp.Diff(tc.want, got, gocmp.AllowUnexported(policy.ConsumedLimit{}), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected consumed limits (-want,+got):\n%s", diff)
			}
		})
	}
}
