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

package config

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/util/validation/field"
	configapiv1beta1 "sigs.k8s.io/kueue/apis/config/v1beta1"
	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobs"
)

// The validation is written against the internal shape, so the older API has to
// reach it through decode, conversion and defaulting with its index intact.
func TestLoadV1Beta1RejectsCopyingTheOriginLabel(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := configapi.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := configapiv1beta1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "v1beta1.yaml")
	if err := os.WriteFile(path, []byte(`
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
integrations:
  labelKeysToCopy:
  - team
  - kueue.x-k8s.io/multikueue-origin
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	_, cfg, err := Load(scheme, path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	want := field.ErrorList{field.Invalid(
		field.NewPath("integrations").Child("labelKeysToCopy").Index(1),
		kueueapi.MultiKueueOriginLabel,
		"is written by a Kueue controller and must not be copied onto a Workload")}
	if diff := cmp.Diff(want, Validate(&cfg, scheme, jobs.NewIntegrationManager())); diff != "" {
		t.Errorf("Validate (-want +got):\n%s", diff)
	}
}
