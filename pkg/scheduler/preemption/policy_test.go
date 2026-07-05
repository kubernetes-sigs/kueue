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

package preemption

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
)

func TestCanAlwaysReclaim(t *testing.T) {
	reclaimProtection := &config.PreemptionProtection{
		ReclaimWithinCohort: &config.PreemptionProtectionPolicy{
			MinAdmitDuration: &metav1.Duration{Duration: 10 * time.Minute},
		},
	}

	testCases := map[string]struct {
		reclaimWithinCohort  kueue.PreemptionPolicy
		preemptionProtection *config.PreemptionProtection
		gateEnabled          bool
		want                 bool
	}{
		"Any without protection": {
			reclaimWithinCohort: kueue.PreemptionPolicyAny,
			gateEnabled:         true,
			want:                true,
		},
		"Any with reclaim protection and gate enabled": {
			reclaimWithinCohort:  kueue.PreemptionPolicyAny,
			preemptionProtection: reclaimProtection,
			gateEnabled:          true,
			want:                 false,
		},
		"Any with reclaim protection but gate disabled": {
			reclaimWithinCohort:  kueue.PreemptionPolicyAny,
			preemptionProtection: reclaimProtection,
			gateEnabled:          false,
			want:                 true,
		},
		"Any with protection block but nil reclaim rule": {
			reclaimWithinCohort: kueue.PreemptionPolicyAny,
			preemptionProtection: &config.PreemptionProtection{
				FairSharing: &config.PreemptionProtectionPolicy{
					MinAdmitDuration: &metav1.Duration{Duration: time.Hour},
				},
			},
			gateEnabled: true,
			want:        true,
		},
		"Any with reclaim rule but nil minAdmitDuration": {
			reclaimWithinCohort: kueue.PreemptionPolicyAny,
			preemptionProtection: &config.PreemptionProtection{
				ReclaimWithinCohort: &config.PreemptionProtectionPolicy{},
			},
			gateEnabled: true,
			want:        true,
		},
		"LowerPriority without protection": {
			reclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
			gateEnabled:         true,
			want:                false,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.PreemptionProtection, tc.gateEnabled)
			cq := &schdcache.ClusterQueueSnapshot{
				Preemption: kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: tc.reclaimWithinCohort,
				},
			}
			if got := CanAlwaysReclaim(cq, tc.preemptionProtection); got != tc.want {
				t.Errorf("CanAlwaysReclaim() = %v, want %v", got, tc.want)
			}
		})
	}
}
