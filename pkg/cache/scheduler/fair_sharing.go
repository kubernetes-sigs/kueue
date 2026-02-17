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
	"cmp"
	"math"

	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/resources"
)

const (
	defaultWeight = 1.0
)

// dominantResourceShareNode is a node in the Cohort tree on which we
// can compute its dominantResourceShare.
type dominantResourceShareNode interface {
	// see FairSharing.Weight in the API.
	fairWeight() float64
	hierarchicalResourceNode
}

// DRS contains the DominantResourceShare for some
// node, with convenence methods for precise comparison.
type DRS struct {
	fairWeight       float64
	unweightedRatio  float64
	dominantResource corev1.ResourceName
}

// NegativeDRS is used as a starting point for comparisons.
func NegativeDRS() DRS {
	return DRS{unweightedRatio: -1, dominantResource: "", fairWeight: defaultWeight}
}

// IsZero returns whether the DRS is 0. In other words,
// the node for which this function was called is not
// borrowing any resources.
func (d DRS) IsZero() bool {
	return d.unweightedRatio == 0
}

func (d DRS) isWeightZero() bool {
	return d.fairWeight == 0
}

func (d DRS) PreciseWeightedShare() float64 {
	if d.IsZero() {
		return 0.0
	}
	if d.isWeightZero() {
		return math.Inf(1)
	}
	return d.unweightedRatio / d.fairWeight
}

// CompareDRS compares two DRS values. A lower value
// indicates that the ClusterQueue/Cohort with this
// value should be preferred for scheduling, while
// a higher value preferred for preemption.
func CompareDRS(a, b DRS) int {
	switch {
	case a.zeroWeightBorrows() && b.zeroWeightBorrows():
		return cmp.Compare(a.unweightedRatio, b.unweightedRatio)
	case a.zeroWeightBorrows():
		return 1
	case b.zeroWeightBorrows():
		return -1
	default:
		return cmp.Compare(a.PreciseWeightedShare(), b.PreciseWeightedShare())
	}
}

// roundedWeightedShare returns a value ranging from 0 to math.MaxInt,
// representing the maximum of the ratios of usage above nominal quota
// to the lendable resources in the cohort, among all the resources
// provided by the ClusterQueue, and divided by the weight.  If zero,
// it means that the usage of the ClusterQueue is below the nominal
// quota.  The function also returns the resource name that yielded
// this value.  When the FairSharing weight is 0, and the ClusterQueue
// or Cohort is borrowing, we return math.MaxInt.
func (d DRS) roundedWeightedShare() (int64, corev1.ResourceName) {
	var weightedShare int64
	if d.zeroWeightBorrows() {
		weightedShare = math.MaxInt64
	} else {
		weightedShare = int64(math.Ceil(d.PreciseWeightedShare()))
	}
	return weightedShare, d.dominantResource
}

// zeroWeightBorrows returns whether this DRS represents a
// borrowing state for a ClusterQueue/Cohort with a zero weight.
func (d DRS) zeroWeightBorrows() bool {
	return d.isWeightZero() && !d.IsZero()
}

// FlavorResourceWeights maps each (flavor, resource) pair to its weight multiplier.
// Used to apply per-(flavor, resource) weights when computing DRS.
type FlavorResourceWeights map[resources.FlavorResource]float64

// flavorWeight returns the configured weight for a (flavor, resource) pair,
// defaulting to 1.0 when not configured.
func flavorWeight(weights FlavorResourceWeights, fr resources.FlavorResource) float64 {
	if weights != nil {
		if w, ok := weights[fr]; ok {
			return w
		}
	}
	return 1.0
}

// ComputeFlavorResourceWeights pre-computes the weights map from ResourceFlavor objects.
func ComputeFlavorResourceWeights(flavors map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor) FlavorResourceWeights {
	weights := make(FlavorResourceWeights)
	for flavorRef, flavor := range flavors {
		for rName, qty := range flavor.Spec.ResourceWeights {
			// Deep copy to avoid data races with the informer cache,
			// as AsFloat64Slow is a mutating method.
			qCopy := qty.DeepCopy()
			w := qCopy.AsFloat64Slow()
			if w > 0 {
				weights[resources.FlavorResource{Flavor: flavorRef, Resource: rName}] = w
			}
		}
	}
	return weights
}

func dominantResourceShare(node dominantResourceShareNode, wlReq resources.FlavorResourceQuantities, weights FlavorResourceWeights) DRS {
	drs := DRS{fairWeight: node.fairWeight(), unweightedRatio: 0, dominantResource: ""}
	if !node.HasParent() {
		return drs
	}

	weightedBorrowing := make(map[corev1.ResourceName]float64, len(node.getResourceNode().SubtreeQuota))
	for fr, quota := range node.getResourceNode().SubtreeQuota {
		amountBorrowed := wlReq[fr] + node.getResourceNode().Usage[fr] - quota
		if amountBorrowed > 0 {
			w := flavorWeight(weights, fr)
			weightedBorrowing[fr.Resource] += float64(amountBorrowed) * w
		}
	}
	if len(weightedBorrowing) == 0 {
		return drs
	}

	weightedLendable := calculateWeightedLendable(node.parentHRN(), weights)
	for rName, b := range weightedBorrowing {
		if lr := weightedLendable[rName]; lr > 0 {
			ratio := b * 1000.0 / lr
			// Use alphabetical order to get a deterministic resource name.
			if ratio > drs.unweightedRatio || (ratio == drs.unweightedRatio && rName < drs.dominantResource) {
				drs.unweightedRatio = ratio
				drs.dominantResource = rName
			}
		}
	}
	return drs
}

// calculateWeightedLendable aggregates weighted capacity for resources
// across all FlavorResources.
func calculateWeightedLendable(node hierarchicalResourceNode, weights FlavorResourceWeights) map[corev1.ResourceName]float64 {
	// walk to root
	root := node
	for root.HasParent() {
		root = root.parentHRN()
	}

	lendable := make(map[corev1.ResourceName]float64, len(root.getResourceNode().SubtreeQuota))
	// The root's SubtreeQuota contains all FlavorResources,
	// as we accumulate even 0s in accumulateFromChild.
	for fr := range root.getResourceNode().SubtreeQuota {
		w := flavorWeight(weights, fr)
		lendable[fr.Resource] += float64(potentialAvailable(node, fr)) * w
	}
	return lendable
}

// parseFairWeight parses FairSharing.Weight if it exists,
// or otherwise returns the default value of 1.
func parseFairWeight(fs *kueue.FairSharing) float64 {
	if fs == nil || fs.Weight == nil {
		return defaultWeight
	}

	// We make a deep copy to avoid any data race, as this is a
	// mutating method. Even though parseFairWeight is only called
	// when we're holding the cache's write lock, we race with the
	// informer cache. See
	// https://github.com/kubernetes/apimachinery/blob/da5b06e2fb6698d6db8866899150ec2c1b4518d9/pkg/api/resource/quantity.go#L538-L539
	weightDeepCopy := fs.Weight.DeepCopy()
	return weightDeepCopy.AsFloat64Slow()
}
