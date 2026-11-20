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

package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestLoadAndGenerateLocalQueues(t *testing.T) {
	const configTemplate = `cohorts:
- className: cohort
  count: 1
  queuesSets:
  - className: cq
    count: 2
    nominalQuota: 10
%s    workloadsSets:
%s`
	const threeTemplatesTwice = `    - count: 2
      workloads:
      - className: a
        request: 1
      - className: b
        request: 1
      - className: c
        request: 1
`
	// One template repeated, as in configs/afs: the LocalQueue must change between repetitions.
	const oneTemplateSixTimes = `    - count: 6
      workloads:
      - className: a
        request: 1
`
	testCases := map[string]struct {
		extraFields   string
		workloadsSets string // defaults to threeTemplatesTwice
		wantLoadErr   error
		// wantLQSuffixes nil means a single LocalQueue named exactly like the ClusterQueue.
		wantLQSuffixes    []string
		wantAdmissionMode *kueue.AdmissionMode
	}{
		"localQueuesCount omitted keeps one LocalQueue named like the ClusterQueue": {},
		"localQueuesCount 1 keeps one LocalQueue named like the ClusterQueue": {
			extraFields: "    localQueuesCount: 1\n",
		},
		"localQueuesCount 3 with AFS spreads workloads round-robin across LocalQueues": {
			extraFields:       "    localQueuesCount: 3\n    admissionMode: UsageBasedAdmissionFairSharing\n",
			wantLQSuffixes:    []string{"-0", "-1", "-2"},
			wantAdmissionMode: new(kueue.UsageBasedAdmissionFairSharing),
		},
		"localQueuesCount 3 spreads repetitions of a single workload template across LocalQueues": {
			extraFields:    "    localQueuesCount: 3\n",
			workloadsSets:  oneTemplateSixTimes,
			wantLQSuffixes: []string{"-0", "-1", "-2"},
		},
		"negative localQueuesCount is rejected": {
			extraFields: "    localQueuesCount: -1\n",
			wantLoadErr: cmpopts.AnyError,
		},
		"unknown admissionMode is rejected before anything is created": {
			extraFields: "    admissionMode: UsageBased\n",
			wantLoadErr: cmpopts.AnyError,
		},
		"NoAdmissionFairSharing admissionMode is accepted": {
			extraFields:       "    admissionMode: NoAdmissionFairSharing\n",
			wantAdmissionMode: new(kueue.NoAdmissionFairSharing),
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.yaml")
			wlSets := tc.workloadsSets
			if wlSets == "" {
				wlSets = threeTemplatesTwice
			}
			if err := os.WriteFile(file, fmt.Appendf(nil, configTemplate, tc.extraFields, wlSets), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := LoadConfig(file)
			if diff := cmp.Diff(tc.wantLoadErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("LoadConfig() error (-want,+got):\n%s", diff)
			}
			if err != nil {
				return
			}

			cl := utiltesting.NewClientBuilder().Build()
			if err := Generate(t.Context(), cl, got); err != nil {
				t.Fatalf("Generate() error = %v", err)
			}

			var cqs kueue.ClusterQueueList
			if err := cl.List(t.Context(), &cqs); err != nil {
				t.Fatalf("listing ClusterQueues: %v", err)
			}
			if len(cqs.Items) != 2 {
				t.Fatalf("got %d ClusterQueues, want 2", len(cqs.Items))
			}

			var wantAdmissionScope *kueue.AdmissionScope
			if tc.wantAdmissionMode != nil {
				wantAdmissionScope = &kueue.AdmissionScope{AdmissionMode: *tc.wantAdmissionMode}
			}
			for _, cq := range cqs.Items {
				if diff := cmp.Diff(wantAdmissionScope, cq.Spec.AdmissionScope); diff != "" {
					t.Errorf("ClusterQueue %s admissionScope (-want,+got):\n%s", cq.Name, diff)
				}

				wantLQNames := sets.New(cq.Name)
				if tc.wantLQSuffixes != nil {
					wantLQNames = sets.New[string]()
					for _, suffix := range tc.wantLQSuffixes {
						wantLQNames.Insert(cq.Name + suffix)
					}
				}

				// The generator puts each ClusterQueue's queues and workloads in a namespace named after it.
				var lqs kueue.LocalQueueList
				if err := cl.List(t.Context(), &lqs, client.InNamespace(cq.Name)); err != nil {
					t.Fatalf("listing LocalQueues of ClusterQueue %s: %v", cq.Name, err)
				}
				gotLQNames := sets.New[string]()
				for _, lq := range lqs.Items {
					gotLQNames.Insert(lq.Name)
					if want := kueue.ClusterQueueReference(cq.Name); lq.Spec.ClusterQueue != want {
						t.Errorf("LocalQueue %s points to ClusterQueue %q, want %q", lq.Name, lq.Spec.ClusterQueue, want)
					}
				}
				if diff := cmp.Diff(sets.List(wantLQNames), sets.List(gotLQNames)); diff != "" {
					t.Errorf("LocalQueues of ClusterQueue %s (-want,+got):\n%s", cq.Name, diff)
				}

				var wls kueue.WorkloadList
				if err := cl.List(t.Context(), &wls, client.InNamespace(cq.Name)); err != nil {
					t.Fatalf("listing Workloads of ClusterQueue %s: %v", cq.Name, err)
				}
				if len(wls.Items) != 6 {
					t.Errorf("ClusterQueue %s has %d Workloads, want 6", cq.Name, len(wls.Items))
				}
				perLQ := map[string]int{}
				for _, wl := range wls.Items {
					perLQ[string(wl.Spec.QueueName)]++
				}
				wantPerLQ := map[string]int{}
				for lqName := range wantLQNames {
					wantPerLQ[lqName] = 6 / wantLQNames.Len()
				}
				if diff := cmp.Diff(wantPerLQ, perLQ); diff != "" {
					t.Errorf("Workloads per LocalQueue of ClusterQueue %s (-want,+got):\n%s", cq.Name, diff)
				}
			}
		})
	}
}

func TestCheckedInConfigsLoad(t *testing.T) {
	testCases := map[string]struct {
		wantAFS bool
	}{
		"baseline":    {},
		"large-scale": {},
		"tas":         {},
		"tas-dra":     {},
		"afs":         {wantAFS: true},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join("..", "..", "configs", name, "generator.yaml")
			config, err := LoadConfig(file)
			if err != nil {
				t.Fatalf("LoadConfig(%s) error = %v", file, err)
			}
			if !tc.wantAFS {
				return
			}
			for _, cohort := range config.Cohorts {
				for _, qs := range cohort.QueuesSets {
					if qs.AdmissionMode != kueue.UsageBasedAdmissionFairSharing {
						t.Errorf("queuesSet %s admissionMode = %q, want %q", qs.ClassName, qs.AdmissionMode, kueue.UsageBasedAdmissionFairSharing)
					}
					if qs.LocalQueuesCount <= 1 {
						t.Errorf("queuesSet %s localQueuesCount = %d, want > 1", qs.ClassName, qs.LocalQueuesCount)
					}
				}
			}
		})
	}
}
