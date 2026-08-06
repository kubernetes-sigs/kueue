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

// Package jobframework_test holds the checks that have to see the package the
// way an importer does. Everything else lives in package jobframework.
package jobframework_test

import (
	"context"

	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
)

// The signatures this branch released. A patch release that adds a parameter or
// a return value to either of them stops compiling here rather than in someone
// else's build.
var (
	_ func(client.Object, func(string) bool) = jobframework.ApplyDefaultLocalQueue

	_ func(context.Context, client.Client, events.EventRecorder, client.Object, *kueue.Workload, func() string) error = jobframework.UpdateWorkloadPriority
)
