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

package admissionfairsharing

import (
	"context"
	"math"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/resource"
)

func ResourceWeights(cqAdmissionScope *kueue.AdmissionScope, afsConfig *config.AdmissionFairSharing) (bool, map[corev1.ResourceName]float64) {
	enableAdmissionFs, fsResWeights := false, make(map[corev1.ResourceName]float64)
	if Enabled(afsConfig) && cqAdmissionScope != nil && cqAdmissionScope.AdmissionMode == kueue.UsageBasedAdmissionFairSharing {
		enableAdmissionFs = true
		fsResWeights = afsConfig.ResourceWeights
	}
	return enableAdmissionFs, fsResWeights
}

func calculateAlphaRate(sampling, halfLifeTime float64) float64 {
	if halfLifeTime == 0 {
		return 0.0
	}

	return 1.0 - math.Pow(0.5, sampling/halfLifeTime)
}

func CalculateEntryPenalty(totalRequests corev1.ResourceList, afs *config.AdmissionFairSharing) corev1.ResourceList {
	alpha := calculateAlphaRate(
		afs.UsageSamplingInterval.Seconds(),
		afs.UsageHalfLifeTime.Seconds(),
	)

	return resource.MulByFloat(totalRequests, alpha)
}

// ResolveLQWeight returns the fair-sharing weight of the referenced LocalQueue.
// A missing LocalQueue falls back to the default weight (1.0) so that its
// Workloads keep participating in fair-sharing comparisons.
func ResolveLQWeight(ctx context.Context, c client.Client, lqObjKey client.ObjectKey) (float64, error) {
	var lq kueue.LocalQueue
	if err := c.Get(ctx, lqObjKey, &lq); err != nil {
		if apierrors.IsNotFound(err) {
			ctrl.LoggerFrom(ctx).V(3).Info("LocalQueue is missing, gracefully falling back to the default weight (1.0)", "localQueue", lqObjKey)
			return 1, nil
		}
		return 0, err
	}
	if lq.Spec.FairSharing != nil && lq.Spec.FairSharing.Weight != nil {
		return lq.Spec.FairSharing.Weight.AsApproximateFloat64(), nil
	}
	return 1, nil
}

func Enabled(afsConfig *config.AdmissionFairSharing) bool {
	return afsConfig != nil && features.Enabled(features.AdmissionFairSharing)
}

func CalculateDecayedConsumed(oldUsage, newUsage corev1.ResourceList, elapsed, halfLifeTime float64) corev1.ResourceList {
	alpha := calculateAlphaRate(elapsed, halfLifeTime)
	scaledOldUsage := resource.MulByFloat(oldUsage, 1-alpha)
	scaledNewUsage := resource.MulByFloat(newUsage, alpha)

	return resource.MergeResourceListKeepSum(scaledOldUsage, scaledNewUsage)
}
