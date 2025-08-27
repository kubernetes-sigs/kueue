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

package dra

import (
	"context"
	"sync"
)

var claims = claimRefTracker{
	claims: make(map[ClaimKey]string),
}

type WorkloadEnqueueFunc func(ctx context.Context, namespace, name string)

// ClaimKey uniquely identifies a ResourceClaim within the scope of a ClusterQueue.
// It has the form <clusterQueue>/<namespace>/<claimName>.
type ClaimKey string

type claimRefTracker struct {
	sync.RWMutex
	claims map[ClaimKey]string
}

// addAndCheckWorkLoad adds a claim to the tracker if it is not already present.
// Returns true if the workload is allowed to use the claim, false if there's a conflict.
// This function should be used to check if a workload is allowed to use a claim.
func (t *claimRefTracker) addAndCheckWorkLoad(ck ClaimKey, workloadName string) bool {
	t.Lock()
	defer t.Unlock()
	wl, ok := t.claims[ck]
	if !ok {
		t.claims[ck] = workloadName
		return true
	}
	if wl == workloadName {
		return true
	}
	return false
}
