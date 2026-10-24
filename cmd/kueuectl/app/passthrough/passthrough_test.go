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

package passthrough

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/cli-runtime/pkg/genericiooptions"
)

func TestNewCommands(t *testing.T) {
	streams := genericiooptions.NewTestIOStreamsDiscard()
	commands := NewCommands(nil, streams)

	wantCommands := []string{"get", "delete", "edit", "describe", "patch"}
	var gotCommands []string
	for _, cmd := range commands {
		gotCommands = append(gotCommands, cmd.Name())
	}
	if diff := cmp.Diff(wantCommands, gotCommands); diff != "" {
		t.Errorf("Unexpected commands (-want/+got):\n%s", diff)
	}

	wantSubcommands := []string{"clusterqueue", "cohort", "localqueue", "resourceflavor", "topology", "workload"}
	wantAliases := map[string][]string{
		"workload":       {"kwl", "kueueworkload", "kueueworkloads"},
		"clusterqueue":   {"cq"},
		"localqueue":     {"lq"},
		"resourceflavor": {"rf"},
		"cohort":         {"ch"},
		"topology":       {"topo"},
	}

	for _, cmd := range commands {
		t.Run(cmd.Name(), func(t *testing.T) {
			subcommands := cmd.Commands()
			var gotSubcommands []string
			for _, sub := range subcommands {
				gotSubcommands = append(gotSubcommands, sub.Name())
				if aliases, ok := wantAliases[sub.Name()]; ok {
					if diff := cmp.Diff(aliases, sub.Aliases); diff != "" {
						t.Errorf("Unexpected aliases for %s %s (-want/+got):\n%s", cmd.Name(), sub.Name(), diff)
					}
				}
			}
			if diff := cmp.Diff(wantSubcommands, gotSubcommands); diff != "" {
				t.Errorf("Unexpected subcommands for %s (-want/+got):\n%s", cmd.Name(), diff)
			}
		})
	}
}
