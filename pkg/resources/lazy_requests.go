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

package resources

// LazyRequests wraps a base Requests interface and performs copy-on-write
// (lazy cloning) when mutations occur.
type LazyRequests struct {
	base   Requests
	cached Requests
}

func isEmpty(requests Requests) bool {
	return requests == nil || requests.IsEmpty()
}

func NewLazyRequests(base Requests) LazyRequests {
	return LazyRequests{base: base}
}

// IsEmpty returns true if both the base and cached Requests are empty.
func (l *LazyRequests) IsEmpty() bool {
	return isEmpty(l.base) && isEmpty(l.cached)
}

// Get returns the underlying Requests (either the cached clone if mutated, or base).
func (l *LazyRequests) Get() Requests {
	if !isEmpty(l.cached) {
		return l.cached
	}
	return l.base
}

func (l *LazyRequests) ensureWritable() {
	if isEmpty(l.cached) {
		switch {
		case !isEmpty(l.base):
			l.cached = l.base.Clone()
		default:
			l.cached = CreateEmpty()
		}
	}
}

// Sub subtracts subRequests from the underlying Requests interface,
// cloning base on first write.
func (l *LazyRequests) Sub(subRequests Requests) {
	if isEmpty(subRequests) {
		return
	}
	l.ensureWritable()
	l.cached.Sub(subRequests)
}

// Add adds addRequests to the underlying Requests interface,
// cloning base on first write.
func (l *LazyRequests) Add(addRequests Requests) {
	if isEmpty(addRequests) {
		return
	}
	l.ensureWritable()
	l.cached.Add(addRequests)
}
