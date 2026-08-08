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

package jobs

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/kueue/pkg/controller/jobs/job"
)

func TestNewIntegrationManager(t *testing.T) {
	first := NewIntegrationManager()
	second := NewIntegrationManager()

	if got := first.GetIntegrationsList(); len(got) == 0 {
		t.Fatal("first manager has no built-in integrations")
	}
	if diff := cmp.Diff(first.GetIntegrationsList(), second.GetIntegrationsList()); diff != "" {
		t.Fatalf("built-in integrations differ between managers (-first +second):\n%s", diff)
	}
}

func TestIntegrationManagersKeepEnabledIntegrationsIsolated(t *testing.T) {
	first := NewIntegrationManager()
	second := NewIntegrationManager()
	first.EnableIntegration(job.FrameworkName)

	controller := true
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{{
		APIVersion: batchv1.SchemeGroupVersion.String(),
		Kind:       "Job",
		Controller: &controller,
	}}}}

	if !first.IsOwnerManagedByKueueForObject(pod) {
		t.Error("first manager did not manage its enabled Job integration")
	}
	if second.IsOwnerManagedByKueueForObject(pod) {
		t.Error("second manager observed the first manager's enabled Job integration")
	}
}
