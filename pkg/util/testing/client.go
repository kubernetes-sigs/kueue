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
	"sync"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
)

func NewFakeClient(objs ...client.Object) client.Client {
	return NewClientBuilder().WithObjects(objs...).WithStatusSubresource(objs...).Build()
}

func NewFakeClientSSAAsSM(objs ...client.Object) client.Client {
	return NewClientBuilder().WithObjects(objs...).WithStatusSubresource(objs...).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: TreatSSAAsStrategicMerge}).Build()
}

func NewClientBuilder(addToSchemes ...func(s *runtime.Scheme) error) *fake.ClientBuilder {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kueue.AddToScheme(scheme))
	utilruntime.Must(kueuealpha.AddToScheme(scheme))
	for i := range addToSchemes {
		utilruntime.Must(addToSchemes[i](scheme))
	}

	return fake.NewClientBuilder().WithScheme(scheme).
		WithIndex(&kueue.LocalQueue{}, indexer.QueueClusterQueueKey, indexer.IndexQueueClusterQueue).
		WithIndex(&kueue.Workload{}, indexer.WorkloadQueueKey, indexer.IndexWorkloadQueue).
		WithIndex(&kueue.Workload{}, indexer.WorkloadClusterQueueKey, indexer.IndexWorkloadClusterQueue).
		WithIndex(&kueue.Workload{}, indexer.OwnerReferenceUID, indexer.IndexOwnerUID)
}

type builderIndexer struct {
	*fake.ClientBuilder
}

func (b *builderIndexer) IndexField(_ context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
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

type EventRecorder struct {
	lock           sync.Mutex
	RecordedEvents []EventRecord
}

var _ record.EventRecorder = (*EventRecorder)(nil)

func SortEvents(ei, ej EventRecord) bool {
	if ei.Key.String() != ej.Key.String() {
		return ei.Key.String() < ej.Key.String()
	}
	if ei.EventType != ej.EventType {
		return ei.EventType < ej.EventType
	}
	if ei.Reason != ej.Reason {
		return ei.Reason < ej.Reason
	}
	if ei.Message != ej.Message {
		return ei.Message < ej.Message
	}
	return false
}

func (tr *EventRecorder) Event(object runtime.Object, eventType, reason, message string) {
	tr.generateEvent(object, eventType, reason, message)
}

func (tr *EventRecorder) Eventf(object runtime.Object, eventType, reason, messageFmt string, args ...interface{}) {
	tr.AnnotatedEventf(object, nil, eventType, reason, messageFmt, args...)
}

func (tr *EventRecorder) AnnotatedEventf(targetObject runtime.Object, _ map[string]string, eventType, reason, messageFmt string, args ...interface{}) {
	tr.generateEvent(targetObject, eventType, reason, fmt.Sprintf(messageFmt, args...))
}

func (tr *EventRecorder) generateEvent(targetObject runtime.Object, eventType, reason, message string) {
	tr.lock.Lock()
	defer tr.lock.Unlock()
	key := types.NamespacedName{}
	if cObj, isCObj := targetObject.(client.Object); isCObj {
		key = client.ObjectKeyFromObject(cObj)
	}
	tr.RecordedEvents = append(tr.RecordedEvents, EventRecord{
		Key:       key,
		EventType: eventType,
		Reason:    reason,
		Message:   message,
	})
}

type ssaPatchAsStrategicMerge struct {
	client.Patch
}

func (*ssaPatchAsStrategicMerge) Type() types.PatchType {
	return types.StrategicMergePatchType
}

func wrapSSAPatch(patch client.Patch) client.Patch {
	if patch.Type() == types.ApplyPatchType {
		return &ssaPatchAsStrategicMerge{Patch: patch}
	}
	return patch
}

// TreatSSAAsStrategicMerge - can be used as a SubResourcePatch interceptor function to treat SSA patches as StrategicMergePatchType.
// Note: By doing so the values set in the patch will be updated but the call will have no knowledge of FieldManagement when it
// comes to detecting conflicts between managers or removing fields that are missing from the patch.
func TreatSSAAsStrategicMerge(ctx context.Context, clnt client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	return clnt.SubResource(subResourceName).Patch(ctx, obj, wrapSSAPatch(patch), opts...)
}
