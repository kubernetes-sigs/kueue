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

package recorder

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func TestRecordWLEventAdmissionLatency(t *testing.T) {
	creationTime := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
	tests := map[string]struct {
		events []WLEvent
		wantMs int64
	}{
		"first observed event is admitted": {
			events: []WLEvent{{
				Time:          creationTime.Add(30 * time.Second),
				CreationTime:  creationTime,
				AdmissionTime: creationTime.Add(20 * time.Second),
				Admitted:      true,
			}},
			wantMs: 20_000,
		},
		"pending event precedes admission": {
			events: []WLEvent{
				{
					Time:         creationTime.Add(time.Second),
					CreationTime: creationTime,
				},
				{
					Time:          creationTime.Add(30 * time.Second),
					CreationTime:  creationTime,
					AdmissionTime: creationTime.Add(20 * time.Second),
					Admitted:      true,
				},
			},
			wantMs: 20_000,
		},
		"missing admission timestamp preserves observation times": {
			events: []WLEvent{
				{
					Time:         creationTime.Add(time.Second),
					CreationTime: creationTime,
				},
				{
					Time:         creationTime.Add(30 * time.Second),
					CreationTime: creationTime,
					Admitted:     true,
				},
			},
			wantMs: 29_000,
		},
		"missing creation timestamp preserves observation baseline": {
			events: []WLEvent{
				{
					Time: creationTime.Add(time.Second),
				},
				{
					Time:          creationTime.Add(30 * time.Second),
					AdmissionTime: creationTime.Add(20 * time.Second),
					Admitted:      true,
				},
			},
			wantMs: 29_000,
		},
		"admission before creation preserves observation baseline": {
			events: []WLEvent{
				{
					Time:         creationTime.Add(time.Second),
					CreationTime: creationTime,
				},
				{
					Time:          creationTime.Add(30 * time.Second),
					CreationTime:  creationTime,
					AdmissionTime: creationTime.Add(-time.Second),
					Admitted:      true,
				},
			},
			wantMs: 29_000,
		},
		"object timestamps do not depend on observation clock": {
			events: []WLEvent{{
				Time:          creationTime.Add(-time.Second),
				CreationTime:  creationTime,
				AdmissionTime: creationTime.Add(20 * time.Second),
				Admitted:      true,
			}},
			wantMs: 20_000,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r := New(time.Minute)
			uid := types.UID("test-workload")
			for i := range tc.events {
				tc.events[i].UID = uid
				r.recordWLEvent(&tc.events[i])
			}

			if got := r.Store.WL[uid].TimeToAdmitMs; got != tc.wantMs {
				t.Errorf("TimeToAdmitMs = %d, want %d", got, tc.wantMs)
			}
		})
	}
}

func TestRecordWLEventCreationTimeDoesNotAffectFinishLatency(t *testing.T) {
	creationTime := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
	uid := types.UID("test-workload")
	r := New(time.Minute)
	r.recordWLEvent(&WLEvent{
		Time:          creationTime.Add(30 * time.Second),
		CreationTime:  creationTime,
		AdmissionTime: creationTime.Add(20 * time.Second),
		UID:           uid,
		Admitted:      true,
		Finished:      true,
	})

	if got := r.Store.WL[uid].TimeToFinishedMs; got != 0 {
		t.Errorf("TimeToFinishedMs = %d, want 0", got)
	}
}

func TestRecordWorkloadStateCarriesObjectTimestamps(t *testing.T) {
	creationTime := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
	admissionTime := creationTime.Add(20 * time.Second)
	uid := types.UID("test-workload")
	wl := &kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-workload",
			Namespace:         "default",
			UID:               uid,
			CreationTimestamp: metav1.NewTime(creationTime),
		},
		Status: kueue.WorkloadStatus{
			Conditions: []metav1.Condition{{
				Type:               kueue.WorkloadAdmitted,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.NewTime(admissionTime),
			}},
		},
	}

	r := New(time.Minute)
	r.running.Store(true)
	r.RecordWorkloadState(wl)
	var ev *WLEvent
	select {
	case ev = <-r.wlEvChan:
	default:
		t.Fatal("RecordWorkloadState() did not enqueue an event")
	}

	if !ev.CreationTime.Equal(creationTime) {
		t.Errorf("CreationTime = %v, want %v", ev.CreationTime, creationTime)
	}
	if !ev.AdmissionTime.Equal(admissionTime) {
		t.Errorf("AdmissionTime = %v, want %v", ev.AdmissionTime, admissionTime)
	}
	if !ev.Admitted {
		t.Error("Admitted = false, want true")
	}
}
