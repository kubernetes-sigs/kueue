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

package testing

import (
	"time"

	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// MockQueue captures enqueued reconcile requests for test assertions.
type MockQueue struct {
	Items []reconcile.Request
}

var _ workqueue.TypedRateLimitingInterface[reconcile.Request] = (*MockQueue)(nil)

func (q *MockQueue) Add(item reconcile.Request)     { q.Items = append(q.Items, item) }
func (q *MockQueue) Len() int                       { return len(q.Items) }
func (q *MockQueue) Get() (reconcile.Request, bool) { return reconcile.Request{}, false }
func (q *MockQueue) Done(reconcile.Request)         {}
func (q *MockQueue) ShutDown()                      {}
func (q *MockQueue) ShutDownWithDrain()             {}
func (q *MockQueue) ShuttingDown() bool             { return false }
func (q *MockQueue) AddAfter(item reconcile.Request, _ time.Duration) {
	q.Items = append(q.Items, item)
}
func (q *MockQueue) AddRateLimited(item reconcile.Request) { q.Items = append(q.Items, item) }
func (q *MockQueue) Forget(reconcile.Request)              {}
func (q *MockQueue) NumRequeues(reconcile.Request) int     { return 0 }
