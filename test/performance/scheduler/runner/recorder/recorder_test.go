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

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/types"
)

func TestRecordWLEventLatencies(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	uid := types.UID("wl-uid")

	newEvent := func(offset time.Duration, admitted, finished bool) *WLEvent {
		return &WLEvent{
			Time:           created.Add(offset),
			NamespacedName: types.NamespacedName{Namespace: "ns", Name: "wl"},
			UID:            uid,
			ClassName:      "large",
			CreationTime:   created,
			Admitted:       admitted,
			Finished:       finished,
		}
	}

	cases := map[string]struct {
		events               []*WLEvent
		wantTimeToAdmitMs    int64
		wantTimeToFinishedMs int64
	}{
		"first observed already admitted: latency measured from creation, not from first observation": {
			// The recorder starts (or receives its first event) 30s after the
			// Workload was created and admitted. Measuring from the first observed
			// event would report 0ms.
			events:            []*WLEvent{newEvent(30*time.Second, true, false)},
			wantTimeToAdmitMs: 30_000,
		},
		"first observed before admission: latency still measured from creation": {
			// Even when admission is observed live, the Workload waited from its
			// creation, not from the moment the recorder first saw it.
			events: []*WLEvent{
				newEvent(10*time.Second, false, false),
				newEvent(25*time.Second, true, false),
			},
			wantTimeToAdmitMs: 25_000,
		},
		"admission and finish are both measured from creation": {
			events: []*WLEvent{
				newEvent(5*time.Second, false, false),
				newEvent(20*time.Second, true, false),
				newEvent(50*time.Second, true, true),
			},
			wantTimeToAdmitMs:    20_000,
			wantTimeToFinishedMs: 50_000,
		},
		"admission time is recorded once and not refreshed by later events": {
			events: []*WLEvent{
				newEvent(15*time.Second, true, false),
				newEvent(40*time.Second, true, false),
			},
			wantTimeToAdmitMs: 15_000,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := New(time.Minute)
			for _, ev := range tc.events {
				r.recordWLEvent(ev)
			}

			got, ok := r.Store.WL[uid]
			if !ok {
				t.Fatalf("no WLState recorded for uid %q", uid)
			}
			if diff := cmp.Diff(tc.wantTimeToAdmitMs, got.TimeToAdmitMs); diff != "" {
				t.Errorf("unexpected TimeToAdmitMs (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantTimeToFinishedMs, got.TimeToFinishedMs); diff != "" {
				t.Errorf("unexpected TimeToFinishedMs (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRecordWLEventFallsBackToFirstEventTime(t *testing.T) {
	// Workloads without a creation timestamp (for example, if the field is not
	// populated) must still produce a usable measurement rather than a negative
	// or wildly large one.
	first := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	uid := types.UID("wl-uid")

	r := New(time.Minute)
	r.recordWLEvent(&WLEvent{
		Time:           first,
		NamespacedName: types.NamespacedName{Namespace: "ns", Name: "wl"},
		UID:            uid,
	})
	r.recordWLEvent(&WLEvent{
		Time:           first.Add(12 * time.Second),
		NamespacedName: types.NamespacedName{Namespace: "ns", Name: "wl"},
		UID:            uid,
		Admitted:       true,
	})

	got := r.Store.WL[uid]
	if diff := cmp.Diff(int64(12_000), got.TimeToAdmitMs); diff != "" {
		t.Errorf("unexpected TimeToAdmitMs (-want +got):\n%s", diff)
	}
}

func TestRecordWLEventCountsEvictions(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	uid := types.UID("wl-uid")

	r := New(time.Minute)
	events := []struct {
		offset  time.Duration
		evicted bool
	}{
		{0, false},
		{time.Second, true},
		{2 * time.Second, true},  // still evicted, must not double count
		{3 * time.Second, false}, // requeued
		{4 * time.Second, true},  // evicted again
	}
	for _, e := range events {
		r.recordWLEvent(&WLEvent{
			Time:           created.Add(e.offset),
			NamespacedName: types.NamespacedName{Namespace: "ns", Name: "wl"},
			UID:            uid,
			CreationTime:   created,
			Evicted:        e.evicted,
		})
	}

	if diff := cmp.Diff(int32(2), r.Store.WL[uid].EvictionCount); diff != "" {
		t.Errorf("unexpected EvictionCount (-want +got):\n%s", diff)
	}
}
