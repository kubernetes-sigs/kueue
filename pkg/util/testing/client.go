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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
)

func NewFakeClient(objs ...client.Object) client.Client {
	return NewClientBuilder().WithObjects(objs...).Build()
}

func NewClientBuilder() *fake.ClientBuilder {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := kueue.AddToScheme(scheme); err != nil {
		panic(err)
	}

	return fake.NewClientBuilder().WithScheme(scheme).
		WithIndex(&kueue.LocalQueue{}, indexer.QueueClusterQueueKey, indexer.IndexQueueClusterQueue).
		WithIndex(&kueue.Workload{}, indexer.WorkloadQueueKey, indexer.IndexWorkloadQueue).
		WithIndex(&kueue.Workload{}, indexer.WorkloadClusterQueueKey, indexer.IndexWorkloadClusterQueue)
}
