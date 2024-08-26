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

package cache

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"sigs.k8s.io/kueue/pkg/resources"
)

type CohortSnapshot struct {
	Name    string
	Members sets.Set[*ClusterQueueSnapshot]

	// RequestableResources equals to the sum of LendingLimit when feature LendingLimit enabled.
	RequestableResources resources.FlavorResourceQuantities
	Usage                resources.FlavorResourceQuantities
	Lendable             map[corev1.ResourceName]int64

	// AllocatableResourceGeneration equals to
	// the sum of allocatable generation among its members.
	AllocatableResourceGeneration int64
}
