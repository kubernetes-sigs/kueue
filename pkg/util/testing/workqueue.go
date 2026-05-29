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

// MockTypedRateLimitingInterface captures enqueued reconcile requests for test assertions.
type MockTypedRateLimitingInterface struct {
	Items []reconcile.Request
}

var _ workqueue.TypedRateLimitingInterface[reconcile.Request] = (*MockTypedRateLimitingInterface)(nil)

func (q *MockTypedRateLimitingInterface) Add(item reconcile.Request) {
	q.Items = append(q.Items, item)
}
func (q *MockTypedRateLimitingInterface) Len() int { return len(q.Items) }
func (q *MockTypedRateLimitingInterface) Get() (reconcile.Request, bool) {
	return reconcile.Request{}, false
}
func (q *MockTypedRateLimitingInterface) Done(reconcile.Request) {}
func (q *MockTypedRateLimitingInterface) ShutDown()              {}
func (q *MockTypedRateLimitingInterface) ShutDownWithDrain()     {}
func (q *MockTypedRateLimitingInterface) ShuttingDown() bool     { return false }
func (q *MockTypedRateLimitingInterface) AddAfter(item reconcile.Request, _ time.Duration) {
	q.Items = append(q.Items, item)
}
func (q *MockTypedRateLimitingInterface) AddRateLimited(item reconcile.Request) {
	q.Items = append(q.Items, item)
}
func (q *MockTypedRateLimitingInterface) Forget(reconcile.Request)          {}
func (q *MockTypedRateLimitingInterface) NumRequeues(reconcile.Request) int { return 0 }
