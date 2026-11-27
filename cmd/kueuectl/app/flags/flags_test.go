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

package flags

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/cobra"
)

func TestAddDryRunFlag(t *testing.T) {
	cmd := &cobra.Command{}
	AddDryRunFlag(cmd)

	fn, found := cmd.GetFlagCompletionFunc("dry-run")
	if !found || fn == nil {
		t.Fatalf("No completion function registered for dry-run flag")
	}

	comps, directive := fn(cmd, nil, "")
	wantComps := []string{"none", "server", "client"}
	if diff := cmp.Diff(wantComps, comps); diff != "" {
		t.Errorf("Unexpected completions for dry-run (-want/+got):\n%s", diff)
	}
	if diff := cmp.Diff(cobra.ShellCompDirectiveNoFileComp, directive); diff != "" {
		t.Errorf("Unexpected directive for dry-run (-want/+got):\n%s", diff)
	}
}
