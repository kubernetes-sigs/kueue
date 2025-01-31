/*
Copyright 2025 The Kubernetes Authors.

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

package cache

import (
	"math"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"sigs.k8s.io/kueue/pkg/resources"
)

// dominantResourceShareNode is a node in the Cohort tree on which we
// can compute its dominantResourceShare.
type dominantResourceShareNode interface {
	// see FairSharing.Weight in the API.
	fairWeight() *resource.Quantity
	hierarchicalResourceNode
}

// dominantResourceShare returns a value from 0 to 1,000,000 representing the maximum of the ratios
// of usage above nominal quota to the lendable resources in the cohort, among all the resources
// provided by the ClusterQueue, and divided by the weight.
// If zero, it means that the usage of the ClusterQueue is below the nominal quota.
// The function also returns the resource name that yielded this value.
// Also for a weight of zero, this will return 9223372036854775807.
func dominantResourceShare(node dominantResourceShareNode, wlReq resources.FlavorResourceQuantities) (int, corev1.ResourceName) {
	if !node.HasParent() {
		return 0, ""
	}
	if node.fairWeight().IsZero() {
		return math.MaxInt, ""
	}

	borrowing := make(map[corev1.ResourceName]int64, len(node.getResourceNode().SubtreeQuota))
	for fr, quota := range node.getResourceNode().SubtreeQuota {
		amountBorrowed := wlReq[fr] + node.getResourceNode().Usage[fr] - quota
		if amountBorrowed > 0 {
			borrowing[fr.Resource] += amountBorrowed
		}
	}
	if len(borrowing) == 0 {
		return 0, ""
	}

	var drs int64 = -1
	var dRes corev1.ResourceName

	lendable := node.parentHRN().getResourceNode().calculateLendable()
	for rName, b := range borrowing {
		if lr := lendable[rName]; lr > 0 {
			ratio := b * 1000 / lr
			// Use alphabetical order to get a deterministic resource name.
			if ratio > drs || (ratio == drs && rName < dRes) {
				drs = ratio
				dRes = rName
			}
		}
	}
	dws := drs * 1000 / node.fairWeight().MilliValue()
	return int(dws), dRes
}
