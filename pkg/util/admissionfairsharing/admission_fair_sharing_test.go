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
	"errors"
	"math"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestResolveLQWeight(t *testing.T) {
	errOther := errors.New("other error")
	tests := map[string]struct {
		localQueue *kueue.LocalQueue
		getErr     error
		wantWeight float64
		wantErr    error
	}{
		"configured weight": {
			localQueue: utiltestingapi.MakeLocalQueue("lq", "ns").
				FairSharing(&kueue.FairSharing{Weight: new(resource.MustParse("2"))}).
				Obj(),
			wantWeight: 2,
		},
		"default weight": {
			localQueue: utiltestingapi.MakeLocalQueue("lq", "ns").Obj(),
			wantWeight: 1,
		},
		"missing LocalQueue": {
			wantWeight: 1,
		},
		"other error": {
			getErr:  errOther,
			wantErr: errOther,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			builder := utiltesting.NewClientBuilder()
			if tc.localQueue != nil {
				builder = builder.WithObjects(tc.localQueue)
			}
			if tc.getErr != nil {
				builder = builder.WithInterceptorFuncs(interceptor.Funcs{
					Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
						return tc.getErr
					},
				})
			}

			ctx, _ := utiltesting.ContextWithLog(t)
			gotWeight, err := ResolveLQWeight(ctx, builder.Build(), client.ObjectKey{Namespace: "ns", Name: "lq"})
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("ResolveLQWeight() error = %v, want %v", err, tc.wantErr)
			}
			if gotWeight != tc.wantWeight {
				t.Errorf("ResolveLQWeight() = %v, want %v", gotWeight, tc.wantWeight)
			}
		})
	}
}

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

func TestCalculateEntryPenaltyWithLongHalfLife(t *testing.T) {
	afs := &config.AdmissionFairSharing{
		UsageSamplingInterval: metav1.Duration{Duration: 5 * time.Minute},
		UsageHalfLifeTime:     metav1.Duration{Duration: 168 * time.Hour},
	}

	penalty := CalculateEntryPenalty(corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("2"),
		"nvidia.com/gpu":   resource.MustParse("1"),
	}, afs)

	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, "nvidia.com/gpu"} {
		got := penalty[name]
		if got.Sign() <= 0 {
			t.Errorf("Unexpected %s penalty, expecting a positive value got %s", name, got.String())
		}
	}
}

// A week-long half-life sampled every five minutes drives the decay factor below
// the point where a per-sample contribution reaches one milli-unit.
const (
	longHalfLifeTime = float64(168 * time.Hour / time.Second)
	samplingElapsed  = float64(5 * time.Minute / time.Second)
)

// Regression test: with a long half-life the per-sample contribution is below one
// milli-unit, and must still accumulate across samples instead of being dropped.
func TestCalculateDecayedConsumedAccumulatesSubMilli(t *testing.T) {
	usage := corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("2"),
		"nvidia.com/gpu":   resource.MustParse("1"),
	}

	consumed := corev1.ResourceList{}
	var prevCPU, prevGPU resource.Quantity
	for sample := 1; sample <= 3; sample++ {
		consumed = CalculateDecayedConsumed(consumed, usage, samplingElapsed, longHalfLifeTime)

		cpu := consumed[corev1.ResourceCPU]
		gpu := consumed["nvidia.com/gpu"]
		if cpu.Cmp(prevCPU) <= 0 {
			t.Errorf("sample %d: cpu did not grow, got %s after %s", sample, cpu.String(), prevCPU.String())
		}
		if gpu.Cmp(prevGPU) <= 0 {
			t.Errorf("sample %d: nvidia.com/gpu did not grow, got %s after %s", sample, gpu.String(), prevGPU.String())
		}
		prevCPU, prevGPU = cpu, gpu
	}
}

// Decayed usage must approach current usage on the half-life curve, and must never
// exceed it: accumulated rounding that drifted upwards would inflate a LocalQueue's
// share indefinitely.
func TestCalculateDecayedConsumedConvergesToUsage(t *testing.T) {
	const samplesPerHalfLife = 2016 // 168h / 5m

	usage := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}
	consumed := corev1.ResourceList{}

	// 1 - 0.5^n of the way to usage after n half-lives.
	wantAfterHalfLife := map[int]string{1: "1", 2: "1500m", 3: "1750m"}

	for halfLives := 1; halfLives <= 3; halfLives++ {
		for range samplesPerHalfLife {
			consumed = CalculateDecayedConsumed(consumed, usage, samplingElapsed, longHalfLifeTime)
		}

		got := consumed[corev1.ResourceCPU]
		want := resource.MustParse(wantAfterHalfLife[halfLives])
		// Allow a milli-unit of accumulated rounding over thousands of samples.
		tolerance := resource.MustParse("1m")
		low, high := want.DeepCopy(), want.DeepCopy()
		low.Sub(tolerance)
		high.Add(tolerance)
		if got.Cmp(low) < 0 || got.Cmp(high) > 0 {
			t.Errorf("after %d half-lives: expecting ~%s got %s", halfLives, want.String(), got.String())
		}
		if total := usage[corev1.ResourceCPU]; got.Cmp(total) > 0 {
			t.Errorf("after %d half-lives: consumed %s exceeds usage %s", halfLives, got.String(), total.String())
		}
	}
}

func TestCalculateDecayedConsumedKeepsLargeQuantitiesPositive(t *testing.T) {
	for _, size := range []string{"16Gi", "1Ti", "64Ti"} {
		t.Run(size, func(t *testing.T) {
			consumed := CalculateDecayedConsumed(
				corev1.ResourceList{},
				corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(size)},
				samplingElapsed, longHalfLifeTime)

			mem := consumed[corev1.ResourceMemory]
			if mem.Sign() <= 0 {
				t.Errorf("Unexpected memory, expecting a positive value got %s", mem.String())
			}
		})
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

func TestCalculateUsageWithNonPositiveWeight(t *testing.T) {
	tests := map[string]struct {
		consumed corev1.ResourceList
		lqWeight float64
	}{
		"zero weight, idle queue (no usage)": {
			consumed: corev1.ResourceList{},
			lqWeight: 0,
		},
		"zero weight, active queue": {
			consumed: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
			lqWeight: 0,
		},
		"negative weight": {
			consumed: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
			lqWeight: -1,
		},
	}

	// A non-positive weight must yield +Inf usage so the LocalQueue is sorted
	// last in the admission order, never NaN (which would sort it first).
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := CalculateUsage(tc.consumed, corev1.ResourceList{}, tc.lqWeight, nil)
			if math.IsNaN(got) {
				t.Fatalf("CalculateUsage() = NaN, want +Inf")
			}
			if !math.IsInf(got, 1) {
				t.Errorf("CalculateUsage() = %v, want +Inf", got)
			}
		})
	}
}
