// Copyright The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package queue

import "sigs.k8s.io/controller-runtime/pkg/client"

// NewManagerForUnitTests creates a new Manager for testing purposes.
// This test manager, though exported, is not included in Kueue binary.
// Note that this function is not found when running:
// make build && go tool nm ./bin/manager | grep "NewManager"
func NewManagerForUnitTests(client client.Client, checker StatusChecker, options ...Option) *Manager {
	return NewManager(client, checker, options...)
}
