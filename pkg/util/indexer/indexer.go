/*
Copyright 2022 The Kubernetes Authors.

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

package indexer

import (
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
)

var (
	IndexQueueClusterQueue = func(obj client.Object) []string {
		q := obj.(*kueue.LocalQueue)
		return []string{string(q.Spec.ClusterQueue)}
	}

	IndexWorkloadQueue = func(obj client.Object) []string {
		wl := obj.(*kueue.Workload)
		return []string{wl.Spec.QueueName}
	}

	IndexWorkloadClusterQueue = func(obj client.Object) []string {
		wl := obj.(*kueue.Workload)
		if wl.Spec.Admission == nil {
			return nil
		}
		return []string{string(wl.Spec.Admission.ClusterQueue)}
	}
)
