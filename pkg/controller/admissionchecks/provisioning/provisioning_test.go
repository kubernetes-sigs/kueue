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

package provisioning

import (
	"math"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

const objectNameMaxLength = 253

func TestProvisioningRequestName(t *testing.T) {
	longWorkload := strings.Repeat("w", 200)
	longCheck := kueue.AdmissionCheckReference(strings.Repeat("c", 60))

	cases := map[string]struct {
		workloadName string
		checkName    kueue.AdmissionCheckReference
		attempt      int32
		wantExact    string
	}{
		"short name keeps attempt as suffix": {
			workloadName: "wl",
			checkName:    "check",
			attempt:      1,
			wantExact:    "wl-check-1",
		},
		"short name with later attempt": {
			workloadName: "wl",
			checkName:    "check",
			attempt:      12,
			wantExact:    "wl-check-12",
		},
		"name longer than truncation threshold": {
			workloadName: longWorkload,
			checkName:    longCheck,
			attempt:      1,
		},
		"name longer than truncation threshold with max attempt": {
			workloadName: longWorkload,
			checkName:    longCheck,
			attempt:      math.MaxInt32,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := ProvisioningRequestName(tc.workloadName, tc.checkName, tc.attempt)
			if tc.wantExact != "" && got != tc.wantExact {
				t.Errorf("ProvisioningRequestName() = %q, want %q", got, tc.wantExact)
			}
			if len(got) > objectNameMaxLength {
				t.Errorf("ProvisioningRequestName() length = %d, want <= %d (name=%q)", len(got), objectNameMaxLength, got)
			}

			prefix := getProvisioningRequestNamePrefix(tc.workloadName, tc.checkName)
			if !strings.HasPrefix(got, prefix) {
				t.Errorf("name %q does not start with prefix %q", got, prefix)
			}

			pr := &autoscaling.ProvisioningRequest{ObjectMeta: metav1.ObjectMeta{Name: got}}
			if !matchesWorkloadAndCheck(pr, tc.workloadName, tc.checkName) {
				t.Errorf("created name %q does not match workload %q check %q", got, tc.workloadName, tc.checkName)
			}
			if gotAttempt := getAttempt(logr.Discard(), pr, tc.workloadName, tc.checkName); gotAttempt != tc.attempt {
				t.Errorf("getAttempt() = %d, want %d (name=%q)", gotAttempt, tc.attempt, got)
			}
		})
	}
}

func TestProvisioningRequestNameStablePrefixAcrossAttempts(t *testing.T) {
	workloadName := strings.Repeat("w", 200)
	checkName := kueue.AdmissionCheckReference(strings.Repeat("c", 60))

	name1 := ProvisioningRequestName(workloadName, checkName, 1)
	name2 := ProvisioningRequestName(workloadName, checkName, 2)
	prefix := getProvisioningRequestNamePrefix(workloadName, checkName)

	if !strings.HasPrefix(name1, prefix) || !strings.HasPrefix(name2, prefix) {
		t.Fatalf("attempts do not share prefix %q: got %q and %q", prefix, name1, name2)
	}
	if name1 == name2 {
		t.Fatalf("different attempts produced the same name %q", name1)
	}

	pr1 := &autoscaling.ProvisioningRequest{ObjectMeta: metav1.ObjectMeta{Name: name1}}
	pr2 := &autoscaling.ProvisioningRequest{ObjectMeta: metav1.ObjectMeta{Name: name2}}
	if !matchesWorkloadAndCheck(pr1, workloadName, checkName) || !matchesWorkloadAndCheck(pr2, workloadName, checkName) {
		t.Fatalf("long names were not recognized as belonging to the same workload and check: %q, %q", name1, name2)
	}
	if got := getAttempt(logr.Discard(), pr1, workloadName, checkName); got != 1 {
		t.Errorf("getAttempt(name1) = %d, want 1", got)
	}
	if got := getAttempt(logr.Discard(), pr2, workloadName, checkName); got != 2 {
		t.Errorf("getAttempt(name2) = %d, want 2", got)
	}
}
