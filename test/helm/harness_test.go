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

// Package helm tests how manager configuration values carry across Helm
// upgrades of charts/kueue and the experimental Kueue Populator chart. It runs
// Helm's install and upgrade actions with in-memory release storage and a
// fake Kubernetes client, so no cluster is needed.
package helm

import (
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/chart/common"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/chart/v2/loader"
	kubefake "helm.sh/helm/v4/pkg/kube/fake"
	release "helm.sh/helm/v4/pkg/release/v1"
	releaseutil "helm.sh/helm/v4/pkg/release/v1/util"
	"helm.sh/helm/v4/pkg/storage"
	"helm.sh/helm/v4/pkg/storage/driver"
	"sigs.k8s.io/yaml"
)

const (
	releaseName = "kueue"
	namespace   = "kueue-system"
	absent      = "<absent>"
	null        = "<nil>"

	kueueConfigMap     = releaseName + "-manager-config"
	populatorConfigMap = releaseName + "-kueue-populator-config"
)

var (
	kueueDir     = filepath.Join("..", "..", "charts", "kueue")
	populatorDir = filepath.Join("..", "..", "cmd", "experimental", "kueue-populator", "charts", "kueue-populator")
)

// A chartBuilder returns a fresh chart for each step, because upgrades with
// reuse flags replace a chart's values in place.
type chartBuilder func(t *testing.T) *chart.Chart

func kueueChart(t *testing.T) *chart.Chart {
	t.Helper()
	c, err := loader.Load(kueueDir)
	if err != nil {
		t.Fatalf("Loading the Kueue chart: %v", err)
	}
	return c
}

// legacyYAMLChart stands in for a chart release before structured overrides,
// whose values shipped the manager configuration as a YAML string.
func legacyYAMLChart(t *testing.T) *chart.Chart {
	c := kueueChart(t)
	c.Values["managerConfig"] = map[string]any{"controllerManagerConfigYaml": `apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
clientConnection:
  qps: 50
  burst: 100
`}
	return c
}

// newDefaultChart is the Kueue chart with a changed default burst.
func newDefaultChart(t *testing.T) *chart.Chart {
	c := kueueChart(t)
	for _, f := range c.Templates {
		if f.Name == "templates/manager/_config.tpl" {
			f.Data = []byte(strings.Replace(string(f.Data), "burst: 500", "burst: 501", 1))
		}
	}
	return c
}

// populatorChart attaches the Kueue chart from this repository as the
// Populator's dependency, so the test needs no `helm dependency build`.
func populatorChart(t *testing.T) *chart.Chart {
	t.Helper()
	c, err := loader.Load(populatorDir)
	if err != nil {
		t.Fatalf("Loading the Populator chart: %v", err)
	}
	c.SetDependencies(kueueChart(t))
	return c
}

// noSchemaChart stands in for a chart release without values.schema.json,
// which saved manager inputs without validating them.
func noSchemaChart(t *testing.T) *chart.Chart {
	c := kueueChart(t)
	c.Schema = nil
	return c
}

// legacyPopulatorChart is the Populator chart with a Kueue dependency from
// before structured overrides.
func legacyPopulatorChart(t *testing.T) *chart.Chart {
	c := populatorChart(t)
	c.SetDependencies(legacyYAMLChart(t))
	return c
}

// wrapperDefaultChart is the Populator chart with a structured override in
// its own values, as a wrapper chart would ship.
func wrapperDefaultChart(t *testing.T) *chart.Chart {
	c := populatorChart(t)
	c.Values["kueue"].(map[string]any)["managerConfig"] = map[string]any{
		"structuredOverrides": map[string]any{"clientConnection": map[string]any{"qps": 600}},
	}
	return c
}

type helmCommand int

const (
	installCommand helmCommand = iota
	upgradeCommand
	reuseValuesCommand
	resetValuesCommand
	resetThenReuseValuesCommand
)

func (c helmCommand) String() string {
	return [...]string{
		"install",
		"upgrade",
		"upgrade --reuse-values",
		"upgrade --reset-values",
		"upgrade --reset-then-reuse-values",
	}[c]
}

type step struct {
	command helmCommand
	chart   chartBuilder
	values  string
}

func install(c chartBuilder, values string) step { return step{installCommand, c, values} }

// upgrade runs an upgrade without reuse or reset flags.
func upgrade(c chartBuilder, values string) step { return step{upgradeCommand, c, values} }

func reuseValues(c chartBuilder, values string) step { return step{reuseValuesCommand, c, values} }

func resetValues(c chartBuilder, values string) step { return step{resetValuesCommand, c, values} }

func resetThenReuseValues(c chartBuilder, values string) step {
	return step{resetThenReuseValuesCommand, c, values}
}

// upgradeCase runs its steps against one release and checks dotted paths in
// the rendered Kueue and Populator manager configurations and in the values
// Helm saved for the last release. With noKueueConfig, the Kueue manager
// ConfigMap must not be rendered. With wantErr, the last step must fail with
// an error containing it.
type upgradeCase struct {
	steps                    []step
	config, populator, saved map[string]string
	noKueueConfig            bool
	wantErr                  string
}

func runCases(t *testing.T, cases map[string]upgradeCase) {
	t.Helper()
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := action.NewConfiguration(action.ConfigurationSetLogger(slog.DiscardHandler))
			cfg.Releases = storage.Init(driver.NewMemory())
			cfg.KubeClient = &kubefake.FailingKubeClient{Out: io.Discard}
			cfg.Capabilities = common.DefaultCapabilities

			var rel *release.Release
			for i, s := range tc.steps {
				var err error
				rel, err = run(t, cfg, s)
				if i == len(tc.steps)-1 && tc.wantErr != "" {
					if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
						t.Fatalf("Running %v: got error %v, want one containing %q", s.command, err, tc.wantErr)
					}
					return
				}
				if err != nil {
					t.Fatalf("Running %v with values %q: %v", s.command, s.values, err)
				}
			}
			configMaps := managerConfigs(t, rel.Manifest)
			if _, rendered := configMaps[kueueConfigMap]; tc.noKueueConfig && rendered {
				t.Errorf("Kueue manager ConfigMap %s rendered, want none", kueueConfigMap)
			}
			check(t, "Kueue manager configuration", tc.config, configMaps[kueueConfigMap])
			check(t, "Populator configuration", tc.populator, configMaps[populatorConfigMap])
			check(t, "saved values", tc.saved, rel.Config)
		})
	}
}

func run(t *testing.T, cfg *action.Configuration, s step) (*release.Release, error) {
	t.Helper()
	values := map[string]any{}
	if err := yaml.Unmarshal([]byte(s.values), &values); err != nil {
		t.Fatalf("Parsing values %q: %v", s.values, err)
	}
	var (
		r   any
		err error
	)
	if s.command == installCommand {
		i := action.NewInstall(cfg)
		i.ReleaseName = releaseName
		i.Namespace = namespace
		r, err = i.Run(s.chart(t), values)
	} else {
		u := action.NewUpgrade(cfg)
		u.Namespace = namespace
		u.ReuseValues = s.command == reuseValuesCommand
		u.ResetValues = s.command == resetValuesCommand
		u.ResetThenReuseValues = s.command == resetThenReuseValuesCommand
		r, err = u.Run(releaseName, s.chart(t), values)
	}
	if err != nil {
		return nil, err
	}
	rel, ok := r.(*release.Release)
	if !ok {
		t.Fatalf("Running %v returned %T, want *release.Release", s.command, r)
	}
	return rel, nil
}

// managerConfigs returns the parsed controller_manager_config.yaml of each
// rendered ConfigMap, keyed by ConfigMap name.
func managerConfigs(t *testing.T, manifest string) map[string]map[string]any {
	t.Helper()
	configs := map[string]map[string]any{}
	for _, doc := range releaseutil.SplitManifests(manifest) {
		var cm struct {
			Kind     string
			Metadata struct{ Name string }
			Data     map[string]string
		}
		if err := yaml.Unmarshal([]byte(doc), &cm); err != nil {
			t.Fatalf("Parsing a rendered manifest: %v", err)
		}
		data, ok := cm.Data["controller_manager_config.yaml"]
		if cm.Kind != "ConfigMap" || !ok {
			continue
		}
		config := map[string]any{}
		if err := yaml.Unmarshal([]byte(data), &config); err != nil {
			t.Fatalf("Parsing %s: %v", cm.Metadata.Name, err)
		}
		configs[cm.Metadata.Name] = config
	}
	return configs
}

func check(t *testing.T, what string, want map[string]string, got map[string]any) {
	t.Helper()
	if want == nil {
		return
	}
	if got == nil {
		t.Fatalf("No %s rendered", what)
	}
	gotPaths := map[string]string{}
	for path := range want {
		gotPaths[path] = lookup(got, path)
	}
	if diff := cmp.Diff(want, gotPaths); diff != "" {
		t.Errorf("Unexpected %s (-want,+got):\n%s", what, diff)
	}
}

func lookup(m map[string]any, path string) string {
	var v any = m
	for key := range strings.SplitSeq(path, ".") {
		parent, ok := v.(map[string]any)
		if !ok {
			return absent
		}
		if v, ok = parent[key]; !ok {
			return absent
		}
	}
	return fmt.Sprint(v)
}
