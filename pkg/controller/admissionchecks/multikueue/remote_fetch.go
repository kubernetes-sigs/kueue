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

package multikueue

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

type listItemsGetter[T any, PL client.ObjectList] interface {
	Items(PL) []T
}

type lqItemsGetter struct{}

func (lqItemsGetter) Items(l *kueue.LocalQueueList) []kueue.LocalQueue {
	return l.Items
}

type cqItemsGetter struct{}

func (cqItemsGetter) Items(l *kueue.ClusterQueueList) []kueue.ClusterQueue {
	return l.Items
}

// getOrList fetches objects from the given client, restricting to given keys.
// If there's only one given key, it uses API Get.
// Otherwise, it uses List and filters the result.
// (This is useful for remote clients, where Get reduces network traffic).
// Missing keys are ignored (i.e. a "not found" error is never returned).
func getOrList[T any, PT interface {
	*T
	client.Object
}, L any, PL interface {
	*L
	client.ObjectList
}](
	ctx context.Context,
	cli client.Client,
	keys sets.Set[types.NamespacedName],
	getter listItemsGetter[T, PL],
) ([]T, error) {
	if keys.Len() == 0 {
		return nil, nil
	}

	if keys.Len() == 1 {
		key := keys.UnsortedList()[0]
		var obj T
		if err := cli.Get(ctx, key, PT(&obj)); err != nil {
			if client.IgnoreNotFound(err) == nil {
				return nil, nil
			}
			return nil, err
		}
		return []T{obj}, nil
	}

	var list L
	if err := cli.List(ctx, PL(&list)); err != nil {
		return nil, err
	}

	var filtered []T
	for _, item := range getter.Items(PL(&list)) {
		objPtr := PT(&item)
		key := types.NamespacedName{Namespace: objPtr.GetNamespace(), Name: objPtr.GetName()}
		if keys.Has(key) {
			filtered = append(filtered, item)
		}
	}
	return filtered, nil
}
