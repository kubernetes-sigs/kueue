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

package crds

import (
	"bytes"
	"io"
	"os/exec"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestCRDs(t *testing.T) {
	kustcmd := exec.Command("../../../bin/kustomize", "build", "../../../config/crd/")
	wantOut, err := kustcmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}

	wr := bytes.NewBuffer(nil)
	cmd := NewCmd()
	cmd.SetOut(wr)
	err = cmd.Execute()
	if err != nil {
		t.Fatal(err)
	}

	gotOut, err := io.ReadAll(wr)
	if err != nil {
		t.Fatal(err)
	}

	if diff := cmp.Diff(string(wantOut), string(gotOut)); diff != "" {
		t.Errorf("Unexpected output (-want/+got)\n%s", diff)
	}
}
