/*
Copyright 2024 The Kubernetes Authors.

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

package version

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/cli-runtime/pkg/genericiooptions"
)

func TestVersionCmd(t *testing.T) {
	streams, _, out, outErr := genericiooptions.NewTestIOStreams()

	cmd := NewVersionCmd(streams)
	cmd.SetArgs([]string{})

	gotErr := cmd.Execute()
	if diff := cmp.Diff(nil, gotErr, cmpopts.EquateErrors()); diff != "" {
		t.Errorf("Unexpected error (-want/+got)\n%s", diff)
	}

	gotOut := out.String()
	if diff := cmp.Diff("Client Version: v0.0.0-main\n", gotOut); diff != "" {
		t.Errorf("Unexpected output (-want/+got)\n%s", diff)
	}

	gotOutErr := outErr.String()
	if diff := cmp.Diff("", gotOutErr); diff != "" {
		t.Errorf("Unexpected output (-want/+got)\n%s", diff)
	}
}
