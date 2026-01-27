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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

const (
	RunningTimeLabel = "kueue.x-k8s.io/performance-scheduler-running-ms"
	ClassLabel       = "kueue.x-k8s.io/performance-scheduler-class"
	CleanupLabel     = "kueue.x-k8s.io/performance-scheduler-cleanup"
)

// WorkloadTemplate defines a workload to generate (extended for TAS)
type WorkloadTemplate struct {
	ClassName     string `json:"className"`
	RuntimeMs     uint   `json:"runtimeMs"`
	Priority      int32  `json:"priority"`
	Request       string `json:"request"`
	PodCount      int    `json:"podCount"`
	TASConstraint string `json:"tasConstraint"` // "required", "preferred", "balanced"
	TASLevel      string `json:"tasLevel"`
	SliceSize     int32  `json:"sliceSize"` // for balanced placement - pods per slice
}

// WorkloadsSet defines a set of workloads to generate
type WorkloadsSet struct {
	Count              int                `json:"count"`
	CreationIntervalMs uint               `json:"creationIntervalMs"`
	Workloads          []WorkloadTemplate `json:"workloads"`
}

// QueuesSet defines a set of queues to generate
type QueuesSet struct {
	ClassName           string                 `json:"className"`
	Count               int                    `json:"count"`
	NominalQuota        string                 `json:"nominalQuota"`
	BorrowingLimit      string                 `json:"borrowingLimit"`
	ReclaimWithinCohort kueue.PreemptionPolicy `json:"reclaimWithinCohort"`
	WithinClusterQueue  kueue.PreemptionPolicy `json:"withinClusterQueue"`
	WorkloadsSets       []WorkloadsSet         `json:"workloadsSets"`
}

// CohortSet defines a set of cohorts to generate
type CohortSet struct {
	ClassName  string      `json:"className"`
	Count      int         `json:"count"`
	QueuesSets []QueuesSet `json:"queuesSets"`
}

// Config represents the full generator configuration
type Config struct {
	Topology       TopologyConfig       `json:"topology"`
	ResourceFlavor ResourceFlavorConfig `json:"resourceFlavor"`
	Cohorts        []CohortSet          `json:"cohorts"`
}

// LoadConfig loads the generator configuration from file
func LoadConfig(configFile string) (*Config, error) {
	yamlFile, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	config := &Config{}
	if err := yaml.Unmarshal(yamlFile, config); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	return config, nil
}

// Generate creates all resources (topology, nodes, queues, workloads)
func Generate(ctx context.Context, c client.Client, config *Config) error {
	log := ctrl.LoggerFrom(ctx).WithName("generate")
	log.Info("Start generation")
	defer log.Info("End generation")

	// 1. Generate topology infrastructure
	if err := GenerateTopologyNodes(ctx, c, config.Topology); err != nil {
		return fmt.Errorf("generating topology nodes: %w", err)
	}

	if err := GenerateTopologyResource(ctx, c, config.Topology); err != nil {
		return fmt.Errorf("generating topology resource: %w", err)
	}

	if err := GenerateResourceFlavor(ctx, c, config.ResourceFlavor, config.Topology.Name); err != nil {
		return fmt.Errorf("generating resource flavor: %w", err)
	}

	// 2. Generate cohorts, queues, and workloads
	return concurrent(config.Cohorts, func(cs []CohortSet) int { return len(cs) }, func(idx int) error {
		return generateCohortSet(ctx, c, config.Cohorts[idx], config.ResourceFlavor.Name)
	})
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
	for i := range total {
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

func generateWlSet(ctx context.Context, c client.Client, wlSet WorkloadsSet, namespace string, localQueue kueue.LocalQueueName, wlSetIdx int) error {
	delay := time.Duration(wlSet.CreationIntervalMs) * time.Millisecond
	log := ctrl.LoggerFrom(ctx).WithName("generate workload group").WithValues("namespace", namespace, "localQueue", localQueue, "delay", delay)
	log.Info("Start generation")
	defer log.Info("End generation")

	for si := range wlSet.Count {
		for i, wlt := range wlSet.Workloads {
			<-time.After(delay)

			// Create workload with TAS support
			wlName := fmt.Sprintf("%s-%d-%d-%d", wlt.ClassName, wlSetIdx, si, i)

			// Use podCount if specified, otherwise default to 1
			podCount := wlt.PodCount
			if podCount == 0 {
				podCount = 1
			}

			podSetBuilder := utiltestingapi.MakePodSet("main", podCount).
				Request(corev1.ResourceCPU, wlt.Request)

			// Add TAS topology request if specified
			if wlt.TASLevel != "" {
				switch wlt.TASConstraint {
				case "required":
					podSetBuilder = podSetBuilder.RequiredTopologyRequest(wlt.TASLevel)
				case "preferred":
					podSetBuilder = podSetBuilder.PreferredTopologyRequest(wlt.TASLevel)
				case "balanced":
					// Balanced placement uses slice-based topology
					podSetBuilder = podSetBuilder.SliceRequiredTopologyRequest(wlt.TASLevel)
					if wlt.SliceSize > 0 {
						podSetBuilder = podSetBuilder.SliceSizeTopologyRequest(wlt.SliceSize)
					}
				}
			}

			wl := utiltestingapi.MakeWorkload(wlName, namespace).
				Queue(localQueue).
				PodSets(*podSetBuilder.Obj()).
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

func generateQueue(ctx context.Context, c client.Client, qSet QueuesSet, cohortName kueue.CohortReference, flavorName string, queueSetIdx int, queueIndex int) error {
	log := ctrl.LoggerFrom(ctx).WithName("generate queue").WithValues("idx", queueIndex, "prefix", qSet.ClassName)
	log.Info("Start generation")
	defer log.Info("End generation")

	cq := utiltestingapi.MakeClusterQueue(fmt.Sprintf("%s-%d-%d-%s", qSet.ClassName, queueSetIdx, queueIndex, cohortName)).
		Cohort(cohortName).
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavorName).
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

	lq := utiltestingapi.MakeLocalQueue(cq.Name, ns.Name).ClusterQueue(cq.Name).Obj()
	err = c.Create(ctx, lq)
	if err != nil {
		return err
	}

	return concurrent(qSet.WorkloadsSets, func(wlSets []WorkloadsSet) int { return len(wlSets) }, func(wlSetIdx int) error {
		return generateWlSet(ctx, c, qSet.WorkloadsSets[wlSetIdx], ns.Name, kueue.LocalQueueName(lq.Name), wlSetIdx)
	})
}

func generateQueueSet(ctx context.Context, c client.Client, qSet QueuesSet, cohortName kueue.CohortReference, flavorName string, queueSetIdx int) error {
	log := ctrl.LoggerFrom(ctx).WithName("generate queue set").WithValues("count", qSet.Count, "prefix", qSet.ClassName)
	log.Info("Start generation")
	defer log.Info("End generation")
	return concurrent(qSet, func(qs QueuesSet) int { return qs.Count }, func(idx int) error {
		return generateQueue(ctx, c, qSet, cohortName, flavorName, queueSetIdx, idx)
	})
}

func generateCohort(ctx context.Context, c client.Client, cSet CohortSet, flavorName string, cohortIdx int) error {
	log := ctrl.LoggerFrom(ctx).WithName("generate cohort").WithValues("idx", cohortIdx, "prefix", cSet.ClassName)
	log.Info("Start generation")
	defer log.Info("End generation")
	cohortName := fmt.Sprintf("%s-%d", cSet.ClassName, cohortIdx)
	return concurrent(cSet, func(cs CohortSet) int { return len(cs.QueuesSets) }, func(idx int) error {
		return generateQueueSet(ctx, c, cSet.QueuesSets[idx], kueue.CohortReference(cohortName), flavorName, idx)
	})
}

func generateCohortSet(ctx context.Context, c client.Client, cSet CohortSet, flavorName string) error {
	log := ctrl.LoggerFrom(ctx).WithName("generate cohort set").WithValues("count", cSet.Count, "prefix", cSet.ClassName)
	log.Info("Start generation")
	defer log.Info("End generation")
	return concurrent(cSet, func(cs CohortSet) int { return cs.Count }, func(idx int) error {
		return generateCohort(ctx, c, cSet, flavorName, idx)
	})
}

// Cleanup removes all generated resources
func Cleanup(ctx context.Context, c client.Client) {
	log := ctrl.LoggerFrom(ctx).WithName("Cleanup")
	log.Info("Start cleanup")
	defer log.Info("End cleanup")

	// Delete all resources with cleanup label
	_ = c.DeleteAllOf(ctx, &kueue.Workload{}, client.HasLabels{CleanupLabel})
	_ = c.DeleteAllOf(ctx, &kueue.LocalQueue{})
	_ = c.DeleteAllOf(ctx, &kueue.ClusterQueue{}, client.HasLabels{CleanupLabel})
	_ = c.DeleteAllOf(ctx, &corev1.Namespace{}, client.HasLabels{CleanupLabel})
	_ = c.DeleteAllOf(ctx, &kueue.ResourceFlavor{}, client.HasLabels{CleanupLabel})
	_ = c.DeleteAllOf(ctx, &kueue.Topology{})
	_ = c.DeleteAllOf(ctx, &corev1.Node{}, client.HasLabels{tasNodeGroupLabel})
}
