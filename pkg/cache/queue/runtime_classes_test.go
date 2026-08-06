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

package queue

import (
	"testing"

	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRuntimeClasses(t *testing.T) {
	rc1 := &nodev1.RuntimeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "runtime-1",
		},
		Handler: "handler-1",
	}

	rcs := newRuntimeClasses()

	// Add RuntimeClass
	rcs.Add(rc1)
	got := rcs.Get("runtime-1")
	if got == nil || got.Name != "runtime-1" {
		t.Errorf("Expected RuntimeClass 'runtime-1', got %v", got)
	}

	all := rcs.GetAll()
	if len(all) != 1 || all["runtime-1"].Name != "runtime-1" {
		t.Errorf("Expected 1 RuntimeClass in GetAll, got %v", all)
	}

	// Delete RuntimeClass
	rcs.Delete(rc1)
	got = rcs.Get("runtime-1")
	if got != nil {
		t.Errorf("Expected nil after delete, got %v", got)
	}
}
