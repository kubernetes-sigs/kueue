package config

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"

	configapiv1beta1 "sigs.k8s.io/kueue/apis/config/v1beta1"
	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
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
	errs := Validate(&cfg, scheme, jobframework.NewIntegrationManager())
	if len(errs) == 0 {
		t.Fatal("Validate() accepted the reserved key from a v1beta1 configuration")
	}
	var found bool
	for _, e := range errs {
		if e.Field == "integrations.labelKeysToCopy[1]" {
			found = true
		}
	}
	if !found {
		t.Errorf("no error names integrations.labelKeysToCopy[1]; got %v", errs.ToAggregate())
	}
}
