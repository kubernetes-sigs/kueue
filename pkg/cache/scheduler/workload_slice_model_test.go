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

package scheduler

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

// TestReplacedWorkloadSliceUsageModel sends random sequences of cache events for a chain of
// workload slices and unrelated Workloads, and checks after every event that:
//   - every Workload holding a reservation is tracked exactly once, as charged or as replaced,
//     and nothing else is tracked;
//   - the ClusterQueue's usage is the sum of the charged Workloads;
//   - a replaced slice has a charged slice of its chain that charges at least as much;
//   - Workloads of other jobs are charged;
//   - in event orders the controllers produce, at most one slice of the chain is charged.
//
// In a quarter of the sequences all slices are created in the same second, so their order is
// unknown to the cache and it may charge more than one slice, but never too little. In half of
// them the chain shrinks, so an older slice covers the newer ones.
func TestReplacedWorkloadSliceUsageModel(t *testing.T) {
	const (
		steps    = 40
		chainLen = 4
	)
	// KUEUE_SLICE_MODEL_SEQUENCES raises the number of sequences for a deeper local run.
	sequences := 1000
	if n, err := strconv.Atoi(os.Getenv("KUEUE_SLICE_MODEL_SEQUENCES")); err == nil && n > 0 {
		sequences = n
	}
	now := time.Now().Truncate(time.Second)
	cq := utiltestingapi.MakeClusterQueue("cq").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "100").Obj()).
		Obj()
	cpu := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}
	jobGVK := batchv1.SchemeGroupVersion.WithKind("Job")
	// The entities: slices s0 <- s1 <- s2 <- s3 of one job, x of another job, and r of another
	// job, which reuses s0's key.
	type entity struct {
		name, key, uid, owner string
		cpu, slice            int // slice is the index in the chain, or -1
	}
	var entities []entity
	for i := range chainLen {
		entities = append(entities, entity{name: fmt.Sprintf("s%d", i), key: fmt.Sprintf("ns/s%d", i), uid: fmt.Sprintf("s%d", i), owner: "job", cpu: i + 1, slice: i})
	}
	entities = append(entities,
		entity{name: "x", key: "ns/x", uid: "x", owner: "other", cpu: 7, slice: -1},
		entity{name: "s0", key: "ns/s0", uid: "r", owner: "reuse", cpu: 11, slice: -1})
	// objects[i][sameSecond][shrinks][finished] is the reserved Workload of entity i. Every slice of a
	// chain carries the chain's slice name, and every Workload here has its job as controller. In a
	// shrinking chain, slice s0 requests 4 cpu and s3 1.
	base := utiltestingapi.MakeWorkload("", "ns").Annotation(kueue.WorkloadSliceNameAnnotation, "s0")
	objects := make([][2][2][2]*kueue.Workload, len(entities))
	cpuOf := make([][2]int, len(entities))
	for i, e := range entities {
		cpuOf[i] = [2]int{e.cpu, e.cpu}
		if e.slice >= 0 {
			cpuOf[i][1] = chainLen - e.slice
		}
		for sameSecond := range 2 {
			for shrinks := range 2 {
				q := strconv.Itoa(cpuOf[i][shrinks])
				w := base.Clone().Name(e.name).UID(types.UID(e.uid)).
					Creation(now.Add(-time.Duration((chainLen-e.slice)*(1-sameSecond))*time.Second)).
					Request(corev1.ResourceCPU, q).
					ControllerReference(jobGVK, e.owner, e.owner+"-uid").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "default", q).Obj()).Obj(), now)
				if e.slice > 0 {
					w = w.Annotation(constants.WorkloadSliceReplacementForAnnotation, fmt.Sprintf("ns/s%d", e.slice-1))
				}
				objects[i][sameSecond][shrinks] = [2]*kueue.Workload{w.Clone().Obj(), w.Clone().Finished().Obj()}
			}
		}
	}

	for seed := range uint64(sequences) {
		rng := rand.New(rand.NewPCG(seed, 0))
		sameSecond := rng.IntN(4) / 3 // 1 in a quarter of the sequences
		shrinks := rng.IntN(2)
		ctx, log := utiltesting.ContextWithLog(t)
		cache := New(utiltesting.NewFakeClient())
		cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
		if err := cache.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("Adding ClusterQueue: %v", err)
		}
		c := cache.hm.ClusterQueue("cq")

		present := make([]bool, len(entities))
		finished := make([]bool, len(entities))
		realistic, jobFinished := true, false
		var history []string
		fail := func(format string, args ...any) {
			t.Fatalf("seed %d, after %s: %s", seed, strings.Join(history, ", "), fmt.Sprintf(format, args...))
		}
		holder := func(key string) int { // the present entity holding key, or -1
			for i, e := range entities {
				if present[i] && e.key == key {
					return i
				}
			}
			return -1
		}

		for range steps {
			i := rng.IntN(len(entities))
			e := entities[i]
			switch op := rng.IntN(4); {
			case op == 0 && !present[i] && !finished[i] && holder(e.key) == -1:
				// A slice is created after its predecessor was admitted, before any later slice, and
				// not once the job has finished.
				if e.slice > 0 && !present[i-1] && !finished[i-1] || e.slice >= 0 && jobFinished {
					realistic = false
				}
				for j := i + 1; e.slice >= 0 && j < chainLen; j++ {
					if present[j] || finished[j] {
						realistic = false
					}
				}
				cache.AddOrUpdateWorkload(ctx, log, objects[i][sameSecond][shrinks][0].DeepCopy())
				present[i] = true
				history = append(history, "add "+e.uid)
			case op == 1 && present[i]:
				cache.AddOrUpdateWorkload(ctx, log, objects[i][sameSecond][shrinks][0].DeepCopy())
				history = append(history, "update "+e.uid)
			case op == 2 && present[i]:
				// A slice finishes once its replacement is admitted, or when the job finishes.
				if e.slice >= 0 && !slices.Contains(present[i+1:chainLen], true) {
					jobFinished = true
				}
				cache.AddOrUpdateWorkload(ctx, log, objects[i][sameSecond][shrinks][1].DeepCopy())
				present[i], finished[i] = false, true
				history = append(history, "finish "+e.uid)
			case op == 3 && present[i]:
				if err := cache.DeleteWorkload(log, workload.Reference(e.key)); err != nil {
					fail("deleting %s: %v", e.key, err)
				}
				present[i] = false
				history = append(history, "delete "+e.uid)
			default:
				continue
			}

			// Every present Workload is tracked once, as charged or replaced, and nothing else.
			want := map[workload.Reference]string{}
			for j, e := range entities {
				if present[j] {
					want[workload.Reference(e.key)] = e.uid
				}
			}
			got := map[workload.Reference]string{}
			for k, wi := range c.Workloads {
				got[k] = string(wi.Obj.UID)
			}
			for k, wi := range c.replacedSlices {
				if _, dup := got[k]; dup {
					fail("%s is both charged and replaced", k)
				}
				got[k] = string(wi.Obj.UID)
			}
			if !maps.Equal(want, got) {
				fail("tracked %v, want %v", got, want)
			}

			// The usage is the sum of the charged Workloads.
			var sum int64
			for _, wi := range c.Workloads {
				q, _ := wi.ResourceUsage().Assigned[cpu].Int64()
				sum += q
			}
			if c.resourceNode.Usage[cpu].CmpInt64(sum) != 0 {
				fail("usage %s, want %dm from %v", c.resourceNode.Usage[cpu].String(), sum, slices.Sorted(maps.Keys(c.Workloads)))
			}

			charged := 0
			for j, e := range entities {
				if !present[j] {
					continue
				}
				_, isCharged := c.Workloads[workload.Reference(e.key)]
				if e.slice < 0 {
					if !isCharged {
						fail("%s of another job is not charged", e.uid)
					}
					continue
				}
				if isCharged {
					charged++
					continue
				}
				// A replaced slice is covered by a charged slice of its chain that charges at least as much.
				covered := false
				for k := range chainLen {
					if _, ok := c.Workloads[workload.Reference(entities[k].key)]; ok && present[k] && k != j && cpuOf[k][shrinks] >= cpuOf[j][shrinks] {
						covered = true
					}
				}
				if !covered {
					fail("%s is replaced, but no charged slice of its chain covers it", e.uid)
				}
			}
			if realistic && sameSecond == 0 && charged > 1 {
				fail("%d slices of the chain are charged", charged)
			}
		}
	}
}
