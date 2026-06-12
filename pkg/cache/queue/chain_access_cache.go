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
	"sync"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

type QuotaCalculationData struct {
	spec                DataPoint[[]kueue.ResourceGroup]
	effectiveRGs        DataPoint[[]kueue.ResourceGroup]
	intermediateDataPts []DataPoint[any]
	dataPointMediators  []DataPointMediator[any, any]
}

type DataPointMediator[S any, D any] struct {
	sync.Mutex
	source                    *DataPoint[S]
	dest                      *DataPoint[D]
	triggerNextTransformation func()
}

type DataPoint[T any] struct {
	sync.RWMutex
	data      T
	newChange bool
}

func (m *DataPointMediator[S, D]) SourceChanged() bool {
	return m.source.newChange
}

func (m *DataPointMediator[S, D]) GetSourceData() S {
	return m.source.data
}

func (m *DataPointMediator[S, D]) AckSourceChange() {
	m.source.newChange = false
}

func (m *DataPointMediator[S, D]) GetDestData() D {
	return m.dest.data
}

func (m *DataPointMediator[S, D]) SaveToDest(data D) {
	m.dest.data = data
	m.dest.newChange = true
	m.triggerNextTransformation()
}

func (m *DataPointMediator[S, D]) Lock() {
	m.Lock()
	defer m.Unlock()
	m.source.RLock()
	m.dest.Lock()
}

func (m *DataPointMediator[S, D]) Unlock() {
	m.Lock()
	defer m.Unlock()
	m.source.RUnlock()
	m.dest.Unlock()
}
