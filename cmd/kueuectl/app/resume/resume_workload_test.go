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

package resume

import (
	"testing"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	"sigs.k8s.io/kueue/client-go/clientset/versioned/fake"
	cmdtesting "sigs.k8s.io/kueue/cmd/kueuectl/app/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestWorkloadCompletionStopsAfterFirstArgument(t *testing.T) {
	clientGetter := cmdtesting.NewTestClientGetter().WithKueueClientset(fake.NewSimpleClientset(
		utiltestingapi.MakeWorkload("wl1", metav1.NamespaceDefault).Active(false).Obj(),
		utiltestingapi.MakeWorkload("wl2", metav1.NamespaceDefault).Active(false).Obj(),
	))
	streams, _, _, _ := genericiooptions.NewTestIOStreams()
	cmd := NewWorkloadCmd(clientGetter, streams)

	names, directive := cmd.ValidArgsFunction(cmd, []string{"wl1"}, "")
	if len(names) != 0 {
		t.Errorf("Unexpected names: %v", names)
	}
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("Unexpected directive: %v", directive)
	}
}
