/*
Copyright 2024 The Kubernetes Authors.

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
	"context"
	"sync/atomic"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

type CQStatus struct {
	Name               string
	PendingWorkloads   int32
	ReservingWorkloads int32
	Active             bool
}

type Store map[string]CQStatus

type Recorder struct {
	maxRecording time.Duration
	running      atomic.Bool
	evChan       chan CQStatus

	Store Store
}

func New(maxRecording time.Duration) *Recorder {
	return &Recorder{
		maxRecording: maxRecording,
		running:      atomic.Bool{},
		evChan:       make(chan CQStatus, 10),
		Store:        map[string]CQStatus{},
	}
}

func (r *Recorder) record(ev CQStatus) {
	r.Store[ev.Name] = ev
}

func (r *Recorder) expectMoreEvents() bool {
	for _, s := range r.Store {
		if (s.PendingWorkloads > 0 || s.ReservingWorkloads > 0) && s.Active {
			return true
		}
	}
	return false
}

func (r *Recorder) Run(ctx context.Context, genDone <-chan struct{}) error {
	r.running.Store(true)
	defer r.running.Store(false)

	generateDone := atomic.Bool{}
	generateDone.Store(false)
	go func() {
		<-genDone
		generateDone.Store(true)
	}()

	ctx, cancel := context.WithTimeout(ctx, r.maxRecording)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-r.evChan:
			r.record(ev)
			if generateDone.Load() && !r.expectMoreEvents() {
				return nil
			}
		}
	}
}

func (r *Recorder) RecordCQStatus(cq *kueue.ClusterQueue) {
	if !r.running.Load() {
		return
	}

	r.evChan <- CQStatus{
		Name:               cq.Name,
		PendingWorkloads:   cq.Status.PendingWorkloads,
		ReservingWorkloads: cq.Status.ReservingWorkloads,
		Active:             apimeta.IsStatusConditionTrue(cq.Status.Conditions, kueue.AdmissionCheckActive),
	}
}
