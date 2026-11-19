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

	"github.com/onsi/gomega"
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
		wantLoadErr   string // substring of the expected LoadConfig error
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
			wantLoadErr: "localQueuesCount",
		},
		"unknown admissionMode is rejected before anything is created": {
			extraFields: "    admissionMode: UsageBased\n",
			wantLoadErr: "admissionMode",
		},
		"NoAdmissionFairSharing admissionMode is accepted": {
			extraFields:       "    admissionMode: NoAdmissionFairSharing\n",
			wantAdmissionMode: new(kueue.NoAdmissionFairSharing),
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			file := filepath.Join(t.TempDir(), "config.yaml")
			wlSets := tc.workloadsSets
			if wlSets == "" {
				wlSets = threeTemplatesTwice
			}
			if err := os.WriteFile(file, fmt.Appendf(nil, configTemplate, tc.extraFields, wlSets), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := LoadConfig(file)
			if tc.wantLoadErr != "" {
				g.Expect(err).To(gomega.MatchError(gomega.ContainSubstring(tc.wantLoadErr)))
				return
			}
			g.Expect(err).NotTo(gomega.HaveOccurred())

			cl := utiltesting.NewClientBuilder().Build()
			g.Expect(Generate(t.Context(), cl, got)).To(gomega.Succeed())

			var cqs kueue.ClusterQueueList
			g.Expect(cl.List(t.Context(), &cqs)).To(gomega.Succeed())
			g.Expect(cqs.Items).To(gomega.HaveLen(2))

			for _, cq := range cqs.Items {
				if tc.wantAdmissionMode == nil {
					g.Expect(cq.Spec.AdmissionScope).To(gomega.BeNil(), "ClusterQueue %s admissionScope", cq.Name)
				} else {
					g.Expect(cq.Spec.AdmissionScope).NotTo(gomega.BeNil(), "ClusterQueue %s admissionScope", cq.Name)
					g.Expect(cq.Spec.AdmissionScope.AdmissionMode).To(gomega.Equal(*tc.wantAdmissionMode))
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
				g.Expect(cl.List(t.Context(), &lqs, client.InNamespace(cq.Name))).To(gomega.Succeed())
				gotLQNames := sets.New[string]()
				for _, lq := range lqs.Items {
					gotLQNames.Insert(lq.Name)
					g.Expect(lq.Spec.ClusterQueue).To(gomega.Equal(kueue.ClusterQueueReference(cq.Name)), "LocalQueue %s", lq.Name)
				}
				g.Expect(gotLQNames).To(gomega.Equal(wantLQNames), "LocalQueues of ClusterQueue %s", cq.Name)

				var wls kueue.WorkloadList
				g.Expect(cl.List(t.Context(), &wls, client.InNamespace(cq.Name))).To(gomega.Succeed())
				g.Expect(wls.Items).To(gomega.HaveLen(6))
				perLQ := map[string]int{}
				for _, wl := range wls.Items {
					perLQ[string(wl.Spec.QueueName)]++
				}
				wantPerLQ := map[string]int{}
				for lqName := range wantLQNames {
					wantPerLQ[lqName] = 6 / wantLQNames.Len()
				}
				g.Expect(perLQ).To(gomega.Equal(wantPerLQ), "workloads per LocalQueue of ClusterQueue %s", cq.Name)
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
			g := gomega.NewWithT(t)
			file := filepath.Join("..", "..", "configs", name, "generator.yaml")
			config, err := LoadConfig(file)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			if !tc.wantAFS {
				return
			}
			for _, cohort := range config.Cohorts {
				for _, qs := range cohort.QueuesSets {
					g.Expect(qs.AdmissionMode).To(gomega.Equal(kueue.UsageBasedAdmissionFairSharing), "queuesSet %s admissionMode", qs.ClassName)
					g.Expect(qs.LocalQueuesCount).To(gomega.BeNumerically(">", 1), "queuesSet %s localQueuesCount", qs.ClassName)
				}
			}
		})
	}
}
