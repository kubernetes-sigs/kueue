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

package features

import (
	"testing"

	"k8s.io/component-base/featuregate"
)

func TestTASResidualPodAccountingAlphaDefault(t *testing.T) {
	specs := defaultVersionedFeatureGates[TASResidualPodAccounting]
	if len(specs) != 1 {
		t.Fatalf("feature has %d version entries, want one Alpha entry", len(specs))
	}
	spec := specs[0]
	if spec.Default || spec.PreRelease != featuregate.Alpha || spec.Version.Major() != 0 || spec.Version.Minor() != 19 {
		t.Errorf("feature spec = %+v, want default-disabled Alpha for 0.19", spec)
	}
	if Enabled(TASResidualPodAccounting) {
		t.Error("Alpha residual Pod accounting must be disabled by default")
	}
}
