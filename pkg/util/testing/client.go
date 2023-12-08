/*
Copyright 2023 The Kubernetes Authors.

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

package testing

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
)

func NewFakeClient(objs ...client.Object) client.Client {
	return NewClientBuilder().WithObjects(objs...).WithStatusSubresource(objs...).Build()
}

func NewClientBuilder(addToSchemes ...func(s *runtime.Scheme) error) *fake.ClientBuilder {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := kueue.AddToScheme(scheme); err != nil {
		panic(err)
	}
	for i := range addToSchemes {
		if err := addToSchemes[i](scheme); err != nil {
			panic(err)
		}
	}

	return fake.NewClientBuilder().WithScheme(scheme).
		WithIndex(&kueue.LocalQueue{}, indexer.QueueClusterQueueKey, indexer.IndexQueueClusterQueue).
		WithIndex(&kueue.Workload{}, indexer.WorkloadQueueKey, indexer.IndexWorkloadQueue).
		WithIndex(&kueue.Workload{}, indexer.WorkloadClusterQueueKey, indexer.IndexWorkloadClusterQueue)
}

type builderIndexer struct {
	*fake.ClientBuilder
}

func (b *builderIndexer) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	b.ClientBuilder = b.ClientBuilder.WithIndex(obj, field, extractValue)
	return nil
}

func AsIndexer(builder *fake.ClientBuilder) client.FieldIndexer {
	return &builderIndexer{ClientBuilder: builder}
}

type EventRecord struct {
	Key       types.NamespacedName
	EventType string
	Reason    string
	Message   string
	// add annotations if ever needed
}

type EventTestRecorder struct {
	RecordedEvents []EventRecord
}

func (tr *EventTestRecorder) Event(object runtime.Object, eventtype, reason, message string) {
	tr.Eventf(object, eventtype, reason, message)
}

func (tr *EventTestRecorder) Eventf(object runtime.Object, eventtype, reason, messageFmt string, args ...interface{}) {
	tr.AnnotatedEventf(object, nil, eventtype, reason, messageFmt, args...)
}

func (tr *EventTestRecorder) AnnotatedEventf(targetObject runtime.Object, annotations map[string]string, eventtype, reason, messageFmt string, args ...interface{}) {
	key := types.NamespacedName{}
	if cobj, iscobj := targetObject.(client.Object); iscobj {
		key = client.ObjectKeyFromObject(cobj)
	}
	tr.RecordedEvents = append(tr.RecordedEvents, EventRecord{
		Key:       key,
		EventType: eventtype,
		Reason:    reason,
		Message:   fmt.Sprintf(messageFmt, args...),
	})
}
