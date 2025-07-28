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
	"math"

	corev1 "k8s.io/api/core/v1"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/resource"
)

func ResourceWeights(cqAdmissionScope *kueue.AdmissionScope, afsConfig *config.AdmissionFairSharing) (bool, map[corev1.ResourceName]float64) {
	enableAdmissionFs, fsResWeights := false, make(map[corev1.ResourceName]float64)
	if features.Enabled(features.AdmissionFairSharing) && afsConfig != nil && cqAdmissionScope != nil && cqAdmissionScope.AdmissionMode == kueue.UsageBasedAdmissionFairSharing {
		enableAdmissionFs = true
		fsResWeights = afsConfig.ResourceWeights
	}
	return enableAdmissionFs, fsResWeights
}

func CalculateAlphaRate(sampling, halfLifeTime float64) float64 {
	if halfLifeTime == 0 {
		return 0.0
	}

	return 1.0 - math.Pow(0.5, sampling/halfLifeTime)
}

func CalculateEntryPenalty(totalRequests corev1.ResourceList, afs *config.AdmissionFairSharing) corev1.ResourceList {
	alpha := CalculateAlphaRate(
		afs.UsageSamplingInterval.Seconds(),
		afs.UsageHalfLifeTime.Seconds(),
	)

	return resource.MulByFloat(totalRequests, alpha)
}
