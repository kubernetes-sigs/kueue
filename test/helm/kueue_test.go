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

package helm

import "testing"

const (
	qps600           = `managerConfig: {structuredOverrides: {clientConnection: {qps: 600}}}`
	burst700         = `managerConfig: {structuredOverrides: {clientConnection: {burst: 700}}}`
	yamlQPS123       = `managerConfig: {controllerManagerConfigYaml: "clientConnection: {qps: 123}"}`
	qpsNull          = `managerConfig: {structuredOverrides: {clientConnection: {qps: null}}}`
	structuredQPS600 = `managerConfig: {mode: structured, structuredOverrides: {clientConnection: {qps: 600}}}`
)

func TestUpgradeFlags(t *testing.T) {
	runCases(t, map[string]upgradeCase{
		"YAML stays the complete configuration with --reuse-values": {
			steps:  []step{install(kueueChart, yamlQPS123), reuseValues(kueueChart, "")},
			config: map[string]string{"clientConnection.qps": "123", "clientConnection.burst": absent},
		},
		"empty YAML stays the legacy empty configuration with --reset-then-reuse-values": {
			steps: []step{
				install(kueueChart, `managerConfig: {controllerManagerConfigYaml: ""}`),
				resetThenReuseValues(kueueChart, ""),
			},
			config: map[string]string{"clientConnection": absent, "internalCertManagement.enable": "true"},
		},
		"--reuse-values keeps an omitted override": {
			steps:  []step{install(kueueChart, qps600), reuseValues(kueueChart, burst700)},
			config: map[string]string{"clientConnection.qps": "600", "clientConnection.burst": "700"},
		},
		"--reset-values drops an omitted override": {
			steps:  []step{install(kueueChart, qps600), resetValues(kueueChart, burst700)},
			config: map[string]string{"clientConnection.qps": "300", "clientConnection.burst": "700"},
		},
		"an upgrade without flags replaces saved values with new values": {
			steps:  []step{install(kueueChart, qps600), upgrade(kueueChart, burst700)},
			config: map[string]string{"clientConnection.qps": "300", "clientConnection.burst": "700"},
		},
		"an upgrade without flags or values keeps saved values": {
			steps:  []step{install(kueueChart, qps600), upgrade(kueueChart, "")},
			config: map[string]string{"clientConnection.qps": "600"},
			saved:  map[string]string{"managerConfig.structuredOverrides.clientConnection.qps": "600"},
		},
		"unset settings follow new chart defaults with --reuse-values": {
			steps:  []step{install(kueueChart, qps600), reuseValues(newDefaultChart, "")},
			config: map[string]string{"clientConnection.qps": "600", "clientConnection.burst": "501"},
		},
		"unset settings follow new chart defaults with --reset-then-reuse-values": {
			steps:  []step{install(kueueChart, qps600), resetThenReuseValues(newDefaultChart, "")},
			config: map[string]string{"clientConnection.qps": "600", "clientConnection.burst": "501"},
		},
		"saved YAML wins over new overrides with --reset-then-reuse-values": {
			steps:  []step{install(kueueChart, yamlQPS123), resetThenReuseValues(kueueChart, qps600)},
			config: map[string]string{"clientConnection.qps": "123", "clientConnection.burst": absent},
		},
	})
}

func TestMigrationFromYAML(t *testing.T) {
	runCases(t, map[string]upgradeCase{
		"retained legacy YAML wins over overrides without mode": {
			steps:  []step{install(legacyYAMLChart, ""), reuseValues(kueueChart, qps600)},
			config: map[string]string{"clientConnection.qps": "50", "clientConnection.burst": "100"},
		},
		"retained legacy YAML wins on every --reuse-values upgrade without mode": {
			steps:  []step{install(legacyYAMLChart, ""), reuseValues(kueueChart, qps600), reuseValues(kueueChart, "")},
			config: map[string]string{"clientConnection.qps": "50", "clientConnection.burst": "100"},
		},
		"an upgrade without flags applies saved overrides over retained legacy YAML": {
			steps:  []step{install(legacyYAMLChart, ""), reuseValues(kueueChart, qps600), upgrade(kueueChart, "")},
			config: map[string]string{"clientConnection.qps": "600", "clientConnection.burst": "500"},
		},
		"structured mode ignores retained legacy YAML": {
			steps:  []step{install(legacyYAMLChart, ""), reuseValues(kueueChart, structuredQPS600)},
			config: map[string]string{"clientConnection.qps": "600", "clientConnection.burst": "500"},
			saved:  map[string]string{"managerConfig.mode": "structured"},
		},
		"a saved structured mode persists when omitted": {
			steps: []step{
				install(legacyYAMLChart, ""),
				reuseValues(kueueChart, structuredQPS600),
				reuseValues(kueueChart, burst700),
			},
			config: map[string]string{"clientConnection.qps": "600", "clientConnection.burst": "700"},
			saved:  map[string]string{"managerConfig.mode": "structured"},
		},
		"YAML mode switches back from structured mode": {
			steps: []step{
				install(legacyYAMLChart, ""),
				reuseValues(kueueChart, structuredQPS600),
				reuseValues(kueueChart, `managerConfig: {mode: yaml, controllerManagerConfigYaml: "clientConnection: {qps: 123}"}`),
			},
			config: map[string]string{"clientConnection.qps": "123", "clientConnection.burst": absent},
		},
		"--reset-values migrates from legacy YAML without mode": {
			steps:  []step{install(legacyYAMLChart, ""), resetValues(kueueChart, qps600)},
			config: map[string]string{"clientConnection.qps": "600", "clientConnection.burst": "500"},
			saved:  map[string]string{"managerConfig.mode": absent},
		},
		"--reset-then-reuse-values drops the old chart's YAML default": {
			steps:  []step{install(legacyYAMLChart, ""), resetThenReuseValues(kueueChart, qps600)},
			config: map[string]string{"clientConnection.qps": "600", "clientConnection.burst": "500"},
		},
	})
}

func TestNullRemoval(t *testing.T) {
	runCases(t, map[string]upgradeCase{
		"null removes a field that was never overridden, and stays saved with --reuse-values": {
			steps:  []step{install(kueueChart, ""), reuseValues(kueueChart, qpsNull), reuseValues(kueueChart, "")},
			config: map[string]string{"clientConnection.qps": absent, "clientConnection.burst": "500"},
			saved:  map[string]string{"managerConfig.structuredOverrides.clientConnection.qps": null},
		},
		"null over a saved override keeps the rendered value but drops it from saved values with --reuse-values": {
			steps:  []step{install(kueueChart, qps600), reuseValues(kueueChart, qpsNull)},
			config: map[string]string{"clientConnection.qps": "600"},
			saved:  map[string]string{"managerConfig.structuredOverrides.clientConnection.qps": absent},
		},
		"null over a saved override takes effect on the next upgrade without reuse flags": {
			steps:  []step{install(kueueChart, qps600), reuseValues(kueueChart, qpsNull), upgrade(kueueChart, "")},
			config: map[string]string{"clientConnection.qps": "300"},
		},
		"null over a saved override restores the default with --reset-then-reuse-values": {
			steps:  []step{install(kueueChart, qps600), resetThenReuseValues(kueueChart, qpsNull)},
			config: map[string]string{"clientConnection.qps": "300"},
		},
		"null removes an overridden field with --reset-values": {
			steps:  []step{install(kueueChart, qps600), resetValues(kueueChart, qpsNull)},
			config: map[string]string{"clientConnection.qps": absent, "clientConnection.burst": "500"},
		},
	})
}

func TestSavedInputValidation(t *testing.T) {
	runCases(t, map[string]upgradeCase{
		"a saved unknown input fails --reuse-values": {
			steps:   []step{install(noSchemaChart, `managerConfig: {typo: 1}`), reuseValues(kueueChart, "")},
			wantErr: "additional properties 'typo' not allowed",
		},
		"a saved unknown input fails --reuse-values even when set to null": {
			steps:   []step{install(noSchemaChart, `managerConfig: {typo: 1}`), reuseValues(kueueChart, `managerConfig: {typo: null}`)},
			wantErr: "additional properties 'typo' not allowed",
		},
		"null removes a saved unknown input with --reset-then-reuse-values": {
			steps:  []step{install(noSchemaChart, `managerConfig: {typo: 1}`), resetThenReuseValues(kueueChart, `managerConfig: {typo: null}`)},
			config: map[string]string{"clientConnection.qps": "300"},
			saved:  map[string]string{"managerConfig.typo": absent},
		},
		"--reset-values drops a saved unknown input": {
			steps:  []step{install(noSchemaChart, `managerConfig: {typo: 1}`), resetValues(kueueChart, "")},
			config: map[string]string{"clientConnection.qps": "300"},
			saved:  map[string]string{"managerConfig.typo": absent},
		},
	})
}
