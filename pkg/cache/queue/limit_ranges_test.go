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

package queue

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestLimitRanges(t *testing.T) {
	lrContainer := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "lr-container",
			Namespace: "ns1",
		},
		Spec: corev1.LimitRangeSpec{
			Limits: []corev1.LimitRangeItem{
				{
					Type: corev1.LimitTypeContainer,
				},
			},
		},
	}

	lrOther := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "lr-other",
			Namespace: "ns1",
		},
		Spec: corev1.LimitRangeSpec{
			Limits: []corev1.LimitRangeItem{
				{
					Type: corev1.LimitTypePersistentVolumeClaim,
				},
			},
		},
	}

	lrs := newLimitRanges()

	// Add container LimitRange
	lrs.Add(lrContainer)
	got := lrs.GetForNamespace("ns1")
	if len(got) != 1 || got[0].Name != "lr-container" {
		t.Errorf("Expected 1 LimitRange 'lr-container', got %v", got)
	}

	// Add non-container/pod LimitRange should not be stored
	lrs.Add(lrOther)
	got = lrs.GetForNamespace("ns1")
	if len(got) != 1 || got[0].Name != "lr-container" {
		t.Errorf("Expected 1 LimitRange 'lr-container', got %v", got)
	}

	// Delete LimitRange
	lrs.Delete(lrContainer)
	got = lrs.GetForNamespace("ns1")
	if len(got) != 0 {
		t.Errorf("Expected 0 LimitRanges, got %v", got)
	}
}
