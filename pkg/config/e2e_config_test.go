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
	"sigs.k8s.io/yaml"

	configapiv1beta1 "sigs.k8s.io/kueue/apis/config/v1beta1"
	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobs"
)

// TestE2EHelmValuesConfigsAreValid loads the controller configuration embedded in
// the Helm e2e values files and validates it. The Helm e2e path is not exercised
// by the presubmits, so a bad value there would otherwise go unnoticed, as a
// fairSharing: {} block did once preemptionStrategies became required.
func TestE2EHelmValuesConfigsAreValid(t *testing.T) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{configapiv1beta1.AddToScheme, configapi.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	integrationManager := jobs.NewIntegrationManager()

	files, err := filepath.Glob(filepath.Join("..", "..", "test", "e2e", "config", "*", "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no Helm e2e values files found")
	}
	for _, file := range files {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var values struct {
				ManagerConfig struct {
					ControllerManagerConfigYaml string `json:"controllerManagerConfigYaml"`
				} `json:"managerConfig"`
			}
			if err := yaml.Unmarshal(raw, &values); err != nil {
				t.Fatalf("parse values.yaml: %v", err)
			}
			embedded := values.ManagerConfig.ControllerManagerConfigYaml
			if embedded == "" {
				t.Skip("no embedded controllerManagerConfigYaml")
			}
			configFile := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(configFile, []byte(embedded), 0600); err != nil {
				t.Fatal(err)
			}
			_, cfg, err := Load(scheme, configFile)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if errs := Validate(&cfg, scheme, integrationManager); len(errs) > 0 {
				t.Errorf("embedded controller config is invalid: %v", errs.ToAggregate())
			}
		})
	}
}
