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

package generator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

const (
	resourceFlavorName = "rf"
	RunningTimeLabel   = "kueue.x-k8s.io/performance-scheduler-running-ms"
	ClassLabel         = "kueue.x-k8s.io/performance-scheduler-class"
	CleanupLabel       = "kueue.x-k8s.io/performance-scheduler-cleanup"
)

type WorkloadTemplate struct {
	ClassName string `json:"className"`
	RuntimeMs uint   `json:"runtimeMs"`
	Priority  int32  `json:"priority"`
	Request   string `json:"request"`
}

type WorkloadsSet struct {
	Count              int                `json:"count"`
	CreationIntervalMs uint               `json:"creationIntervalMs"`
	Workloads          []WorkloadTemplate `json:"workloads"`
}

type QueuesSet struct {
	ClassName           string                 `json:"className"`
	Count               int                    `json:"count"`
	NominalQuota        string                 `json:"nominalQuota"`
	BorrowingLimit      string                 `json:"borrowingLimit"`
	ReclaimWithinCohort kueue.PreemptionPolicy `json:"reclaimWithinCohort"`
	WithinClusterQueue  kueue.PreemptionPolicy `json:"withinClusterQueue"`
	WorkloadsSets       []WorkloadsSet         `json:"workloadsSets"`
}

type CohortSet struct {
	ClassName  string      `json:"className"`
	Count      int         `json:"count"`
	QueuesSets []QueuesSet `json:"queuesSets"`
}

func LoadConfig(mappingFile string) ([]CohortSet, error) {
	cohorts := []CohortSet{}
	yamlFile, err := os.ReadFile(mappingFile)
	if err != nil {
		return nil, err
	}
	err = yaml.Unmarshal(yamlFile, &cohorts)
	if err != nil {
		return nil, fmt.Errorf("decoding %q: %w", mappingFile, err)
	}
	return cohorts, nil
}

func concurrent[T any](set T, count func(T) int, call func(int) error) error {
	wg := sync.WaitGroup{}
	errCh := make(chan error)
	var errs []error
	go func() {
		for err := range errCh {
			errs = append(errs, err)
		}
	}()
	total := count(set)
	wg.Add(total)
	for i := 0; i < total; i++ {
		go func() {
			defer wg.Done()
			err := call(i)
			if err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	return errors.Join(errs...)
}
func generateWlSet(ctx context.Context, c client.Client, wlSet WorkloadsSet, namespace string, localQueue string, wlSetIdx int) error {
	delay := time.Duration(wlSet.CreationIntervalMs) * time.Millisecond
	log := ctrl.LoggerFrom(ctx).WithName("generate workload group").WithValues("namespace", namespace, "localQueue", localQueue, "delay", delay)
	log.Info("Start generation")
	defer log.Info("End generation")

	for si := 0; si < wlSet.Count; si++ {
		for i, wlt := range wlSet.Workloads {
			<-time.After(delay)
			wl := utiltesting.MakeWorkload(fmt.Sprintf("%s-%d-%d-%d", wlt.ClassName, wlSetIdx, si, i), namespace).
				Queue(localQueue).
				Request(corev1.ResourceCPU, wlt.Request).
				Label(RunningTimeLabel, fmt.Sprintf("%d", wlt.RuntimeMs)).
				Label(ClassLabel, wlt.ClassName).
				Priority(wlt.Priority).
				Obj()
			err := c.Create(ctx, wl)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func generateQueue(ctx context.Context, c client.Client, qSet QueuesSet, cohortName string, queueSetIdx int, queueIndex int) error {
	log := ctrl.LoggerFrom(ctx).WithName("generate queue").WithValues("idx", queueIndex, "prefix", qSet.ClassName)
	log.Info("Start generation")
	defer log.Info("End generation")
	cq := utiltesting.MakeClusterQueue(fmt.Sprintf("%s-%d-%d-%s", qSet.ClassName, queueSetIdx, queueIndex, cohortName)).
		Cohort(cohortName).
		ResourceGroup(*utiltesting.MakeFlavorQuotas(resourceFlavorName).
			Resource(corev1.ResourceCPU, qSet.NominalQuota, qSet.BorrowingLimit).Obj()).
		Preemption(kueue.ClusterQueuePreemption{
			ReclaimWithinCohort: qSet.ReclaimWithinCohort,
			WithinClusterQueue:  qSet.WithinClusterQueue,
		}).
		Label(ClassLabel, qSet.ClassName).
		Label(CleanupLabel, "true").
		Obj()
	err := c.Create(ctx, cq)
	if err != nil {
		return err
	}

	ns := &corev1.Namespace{}
	ns.Name = cq.Name
	ns.Labels = map[string]string{CleanupLabel: "true"}
	err = c.Create(ctx, ns)
	if err != nil {
		return err
	}

	lq := utiltesting.MakeLocalQueue(cq.Name, ns.Name).ClusterQueue(cq.Name).Obj()
	err = c.Create(ctx, lq)
	if err != nil {
		return err
	}

	return concurrent(qSet.WorkloadsSets, func(wlSets []WorkloadsSet) int { return len(wlSets) }, func(wlSetIdx int) error {
		return generateWlSet(ctx, c, qSet.WorkloadsSets[wlSetIdx], ns.Name, lq.Name, wlSetIdx)
	})
}

func generateQueueSet(ctx context.Context, c client.Client, qSet QueuesSet, cohortName string, queueSetIdx int) error {
	log := ctrl.LoggerFrom(ctx).WithName("generate queue set").WithValues("count", qSet.Count, "prefix", qSet.ClassName)
	log.Info("Start generation")
	defer log.Info("End generation")
	return concurrent(qSet, func(qs QueuesSet) int { return qs.Count }, func(idx int) error {
		return generateQueue(ctx, c, qSet, cohortName, queueSetIdx, idx)
	})
}

func generateCohort(ctx context.Context, c client.Client, cSet CohortSet, cohortIdx int) error {
	log := ctrl.LoggerFrom(ctx).WithName("generate cohort").WithValues("idx", cohortIdx, "prefix", cSet.ClassName)
	log.Info("Start generation")
	defer log.Info("End generation")
	cohortName := fmt.Sprintf("%s-%d", cSet.ClassName, cohortIdx)
	return concurrent(cSet, func(cs CohortSet) int { return len(cs.QueuesSets) }, func(idx int) error {
		return generateQueueSet(ctx, c, cSet.QueuesSets[idx], cohortName, idx)
	})
}

func generateCohortSet(ctx context.Context, c client.Client, cSet CohortSet) error {
	log := ctrl.LoggerFrom(ctx).WithName("generate cohort set").WithValues("count", cSet.Count, "prefix", cSet.ClassName)
	log.Info("Start generation")
	defer log.Info("End generation")
	return concurrent(cSet, func(cs CohortSet) int { return cs.Count }, func(idx int) error {
		return generateCohort(ctx, c, cSet, idx)
	})
}

func Generate(ctx context.Context, c client.Client, cSets []CohortSet) error {
	log := ctrl.LoggerFrom(ctx).WithName("generate cohort sets").WithValues("numSets", len(cSets))
	log.Info("Start generation")
	defer log.Info("End generation")
	rf := utiltesting.MakeResourceFlavor(resourceFlavorName).NodeLabel(CleanupLabel, "true").Obj()
	err := c.Create(ctx, rf)
	if err != nil {
		return err
	}
	return concurrent(cSets, func(s []CohortSet) int { return len(s) }, func(idx int) error { return generateCohortSet(ctx, c, cSets[idx]) })
}

func Cleanup(ctx context.Context, c client.Client) {
	log := ctrl.LoggerFrom(ctx).WithName("cleanup")
	log.Info("Start cleanup")

	nsList := corev1.NamespaceList{}
	if err := c.List(ctx, &nsList, client.HasLabels{CleanupLabel}); err != nil {
		log.Error(err, "Listing namespaces")
	} else {
		for _, ns := range nsList.Items {
			if err := c.Delete(ctx, &ns); err != nil {
				log.Error(err, "Deleting namespace")
			}
		}
	}

	if err := c.DeleteAllOf(ctx, &kueue.ClusterQueue{}, client.HasLabels{CleanupLabel}); err != nil {
		log.Error(err, "Deleting cluster queues")
	}

	if err := c.DeleteAllOf(ctx, &kueue.ResourceFlavor{}, client.HasLabels{CleanupLabel}); err != nil {
		log.Error(err, "Deleting resource flavor")
	}
}
