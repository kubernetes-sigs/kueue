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
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
)

func TestCalculateEntryPenaltyWithDRAResources(t *testing.T) {
	afs := &config.AdmissionFairSharing{
		UsageSamplingInterval: metav1.Duration{Duration: 5 * time.Minute},
		UsageHalfLifeTime:     metav1.Duration{Duration: 10 * time.Minute},
	}

	totalRequests := corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("4"),
		"gpu-logical":      resource.MustParse("2"),
	}

	penalty := CalculateEntryPenalty(totalRequests, afs)

	// Penalty should include both cpu and DRA logical resource
	if _, exists := penalty[corev1.ResourceCPU]; !exists {
		t.Error("penalty should include cpu resource")
	}
	if _, exists := penalty["gpu-logical"]; !exists {
		t.Error("penalty should include DRA logical resource 'gpu-logical'")
	}

	// Both penalties should be positive (alpha > 0 when halfLifeTime > 0)
	cpuPenalty := penalty[corev1.ResourceCPU]
	if cpuPenalty.Cmp(resource.MustParse("0")) <= 0 {
		t.Errorf("cpu penalty should be positive, got %v", cpuPenalty)
	}
	gpuPenalty := penalty["gpu-logical"]
	if gpuPenalty.Cmp(resource.MustParse("0")) <= 0 {
		t.Errorf("gpu-logical penalty should be positive, got %v", gpuPenalty)
	}
}

func TestCalculateUsageWithDRA(t *testing.T) {
	tests := map[string]struct {
		consumed   corev1.ResourceList
		penalty    corev1.ResourceList
		lqWeight   float64
		resWeights map[corev1.ResourceName]float64
		wantUsage  float64
	}{
		"DRA resource with default weight": {
			consumed: corev1.ResourceList{
				"gpu-logical": resource.MustParse("2"),
			},
			penalty:    corev1.ResourceList{},
			lqWeight:   1,
			resWeights: map[corev1.ResourceName]float64{},
			wantUsage:  2, // default weight is 1, so 1 * 2 / 1 = 2
		},
		"DRA resource with explicit weight": {
			consumed: corev1.ResourceList{
				"gpu-logical": resource.MustParse("2"),
			},
			penalty:  corev1.ResourceList{},
			lqWeight: 1,
			resWeights: map[corev1.ResourceName]float64{
				"gpu-logical": 3.0,
			},
			wantUsage: 6, // 3 * 2 / 1 = 6
		},
		"mixed CPU and DRA resources": {
			consumed: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("4"),
				"gpu-logical":      resource.MustParse("2"),
			},
			penalty:  corev1.ResourceList{},
			lqWeight: 1,
			resWeights: map[corev1.ResourceName]float64{
				corev1.ResourceCPU: 1.0,
				"gpu-logical":      5.0,
			},
			wantUsage: 14, // (1*4 + 5*2) / 1 = 14
		},
		"DRA resource with weight zero contributes nothing": {
			consumed: corev1.ResourceList{
				"gpu-logical": resource.MustParse("10"),
			},
			penalty:  corev1.ResourceList{},
			lqWeight: 1,
			resWeights: map[corev1.ResourceName]float64{
				"gpu-logical": 0,
			},
			wantUsage: 0,
		},
		"DRA resource in penalty only": {
			consumed: corev1.ResourceList{},
			penalty: corev1.ResourceList{
				"gpu-logical": resource.MustParse("3"),
			},
			lqWeight:   1,
			resWeights: map[corev1.ResourceName]float64{},
			wantUsage:  3, // default weight 1, 1 * 3 / 1 = 3
		},
		"DRA resource in both consumed and penalty": {
			consumed: corev1.ResourceList{
				"gpu-logical": resource.MustParse("2"),
			},
			penalty: corev1.ResourceList{
				"gpu-logical": resource.MustParse("1"),
			},
			lqWeight: 2,
			resWeights: map[corev1.ResourceName]float64{
				"gpu-logical": 4.0,
			},
			wantUsage: 6, // 4 * (2+1) / 2 = 6
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := CalculateUsage(tc.consumed, tc.penalty, tc.lqWeight, tc.resWeights)
			if got != tc.wantUsage {
				t.Errorf("CalculateUsage() = %v, want %v", got, tc.wantUsage)
			}
		})
	}
}
