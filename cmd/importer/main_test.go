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

package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestImportCmdRejectsZeroWorkers(t *testing.T) {
	cmd := &cobra.Command{Use: "import", RunE: importCmd}
	setFlags(cmd)
	cmd.SetArgs([]string{"-n", "default", "--queuelabel", "q", "--queuemapping", "a=b", "-c", "0"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected an error for --%s=0, got nil", ConcurrencyFlag)
	}
	if !strings.Contains(err.Error(), ConcurrencyFlag) {
		t.Errorf("error %q should mention the --%s flag", err, ConcurrencyFlag)
	}
}
