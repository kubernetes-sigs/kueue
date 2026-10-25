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

func TestPopulatorAndWrapperCharts(t *testing.T) {
	researchSelector := map[string]string{"managedJobsNamespaceSelector.matchLabels.team": "research"}
	runCases(t, map[string]upgradeCase{
		"the Populator and its Kueue dependency keep sharing overrides with --reuse-values": {
			steps: []step{
				install(populatorChart, `kueue: {enabled: true, managerConfig: {structuredOverrides: {managedJobsNamespaceSelector: {matchLabels: {team: research}}}}}`),
				reuseValues(populatorChart, ""),
			},
			config:    researchSelector,
			populator: researchSelector,
		},
		"the Populator keeps overrides with Kueue disabled and --reuse-values": {
			steps: []step{
				install(populatorChart, `kueue: {managerConfig: {structuredOverrides: {managedJobsNamespaceSelector: {matchLabels: {team: research}}}}}`),
				reuseValues(populatorChart, ""),
			},
			populator:     researchSelector,
			noKueueConfig: true,
		},
		"customer YAML wins over a wrapper chart's structured default": {
			steps: []step{
				install(wrapperDefaultChart, `kueue: {enabled: true, managerConfig: {controllerManagerConfigYaml: "clientConnection: {qps: 123}"}}`),
			},
			config: map[string]string{"clientConnection.qps": "123"},
		},
		"a new wrapper chart default is ignored with --reuse-values": {
			steps:  []step{install(populatorChart, `kueue: {enabled: true}`), reuseValues(wrapperDefaultChart, "")},
			config: map[string]string{"clientConnection.qps": "300"},
		},
		"a new wrapper chart default applies with --reset-then-reuse-values": {
			steps:  []step{install(populatorChart, `kueue: {enabled: true}`), resetThenReuseValues(wrapperDefaultChart, "")},
			config: map[string]string{"clientConnection.qps": "600"},
		},
		"the old Kueue dependency's YAML wins over overrides with --reuse-values": {
			steps: []step{
				install(legacyPopulatorChart, `kueue: {enabled: true}`),
				reuseValues(populatorChart, `kueue: {managerConfig: {structuredOverrides: {managedJobsNamespaceSelector: {matchLabels: {team: research}}}}}`),
			},
			config:    map[string]string{"clientConnection.qps": "50", "managedJobsNamespaceSelector": absent},
			populator: map[string]string{"managedJobsNamespaceSelector.matchLabels": absent},
		},
		"structured mode applies overrides over the old Kueue dependency with --reuse-values": {
			steps: []step{
				install(legacyPopulatorChart, `kueue: {enabled: true}`),
				reuseValues(populatorChart, `kueue: {managerConfig: {mode: structured, structuredOverrides: {managedJobsNamespaceSelector: {matchLabels: {team: research}}}}}`),
			},
			config:    map[string]string{"clientConnection.qps": "300", "managedJobsNamespaceSelector.matchLabels.team": "research"},
			populator: researchSelector,
		},
		"overrides apply over the old Kueue dependency with --reset-then-reuse-values": {
			steps: []step{
				install(legacyPopulatorChart, `kueue: {enabled: true}`),
				resetThenReuseValues(populatorChart, `kueue: {managerConfig: {structuredOverrides: {managedJobsNamespaceSelector: {matchLabels: {team: research}}}}}`),
			},
			config:    map[string]string{"clientConnection.qps": "300", "managedJobsNamespaceSelector.matchLabels.team": "research"},
			populator: researchSelector,
		},
	})
}
