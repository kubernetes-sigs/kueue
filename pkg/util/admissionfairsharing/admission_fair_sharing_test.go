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
