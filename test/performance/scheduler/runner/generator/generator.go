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
	defaultResourceFlavorName = "rf"
	RunningTimeLabel          = "kueue.x-k8s.io/performance-scheduler-running-ms"
	ClassLabel                = "kueue.x-k8s.io/performance-scheduler-class"
	CleanupLabel              = "kueue.x-k8s.io/performance-scheduler-cleanup"
)

type WorkloadTemplate struct {
	ClassName     string `json:"className"`
	RuntimeMs     uint   `json:"runtimeMs"`
	Priority      int32  `json:"priority"`
	Request       string `json:"request"`
	PodCount      int    `json:"podCount"`      // for TAS: number of pods in workload (default 1)
	TASConstraint string `json:"tasConstraint"` // for TAS: "required", "preferred", "balanced"
	TASLevel      string `json:"tasLevel"`      // for TAS: topology level
	SliceSize     int32  `json:"sliceSize"`     // for TAS: pods per slice in balanced placement
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

// Config represents the full generator configuration with optional TAS features
type Config struct {
	Topology       *TopologyConfig       `json:"topology,omitempty"`
	ResourceFlavor *ResourceFlavorConfig `json:"resourceFlavor,omitempty"`
	Cohorts        []CohortSet           `json:"cohorts,omitempty"`
}

func LoadConfig(configFile string) (*Config, error) {
	yamlFile, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	config := &Config{}
	if err := yaml.Unmarshal(yamlFile, config); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if len(config.Cohorts) == 0 {
		return nil, errors.New("config must contain at least one cohort")
	}

	// Set default resource flavor if not provided (for scheduler configs without TAS)
	if config.ResourceFlavor == nil {
		config.ResourceFlavor = &ResourceFlavorConfig{
			Name: defaultResourceFlavorName,
		}
	}

	return config, nil
}

func concurrent[T any](set T, count func(T) int, call func(int) error) error {
	wg := sync.WaitGroup{}
	errCh := make(chan error)
	var errs []error
	done := make(chan struct{})
	go func() {
		for err := range errCh {
			errs = append(errs, err)
		}
		close(done)
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
	<-done
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

			wlName := fmt.Sprintf("%s-%d-%d-%d", wlt.ClassName, wlSetIdx, si, i)

			// Use podCount if specified, otherwise default to 1
			podCount := wlt.PodCount
			if podCount == 0 {
				podCount = 1
			}

			// Build workload with optional TAS features
			wlBuilder := utiltestingapi.MakeWorkload(wlName, namespace).
				Queue(localQueue).
				Label(RunningTimeLabel, fmt.Sprintf("%d", wlt.RuntimeMs)).
				Label(ClassLabel, wlt.ClassName).
				Priority(wlt.Priority)

			// If TAS topology is requested, build PodSet with topology
			if wlt.TASLevel != "" {
				podSetBuilder := utiltestingapi.MakePodSet("main", podCount).
					Request(corev1.ResourceCPU, wlt.Request)

				// Add topology request based on constraint type
				switch wlt.TASConstraint {
				case "required":
					podSetBuilder = podSetBuilder.RequiredTopologyRequest(wlt.TASLevel)
				case "preferred":
					podSetBuilder = podSetBuilder.PreferredTopologyRequest(wlt.TASLevel)
				case "balanced":
					podSetBuilder = podSetBuilder.SliceRequiredTopologyRequest(wlt.TASLevel)
					if wlt.SliceSize > 0 {
						podSetBuilder = podSetBuilder.SliceSizeTopologyRequest(wlt.SliceSize)
					}
				}

				wlBuilder = wlBuilder.PodSets(*podSetBuilder.Obj())
			} else {
				// Standard mode: simple request without PodSet
				wlBuilder = wlBuilder.Request(corev1.ResourceCPU, wlt.Request)
			}

			wl := wlBuilder.Obj()
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

func Generate(ctx context.Context, c client.Client, config *Config) error {
	log := ctrl.LoggerFrom(ctx).WithName("generate").WithValues("numCohortSets", len(config.Cohorts))
	log.Info("Start generation")
	defer log.Info("End generation")

	if config.Topology != nil {
		// TAS mode
		log.Info("Generating TAS topology infrastructure")
		if err := generateTopologyNodes(ctx, c, *config.Topology); err != nil {
			return fmt.Errorf("generating topology nodes: %w", err)
		}

		if err := generateTopologyResource(ctx, c, *config.Topology); err != nil {
			return fmt.Errorf("generating topology resource: %w", err)
		}
	}

	if err := generateResourceFlavor(ctx, c, *config.ResourceFlavor); err != nil {
		return fmt.Errorf("generating resource flavor: %w", err)
	}

	return concurrent(config.Cohorts, func(cs []CohortSet) int { return len(cs) }, func(idx int) error {
		return generateCohortSet(ctx, c, config.Cohorts[idx], config.ResourceFlavor.Name)
	})
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

	if err := c.DeleteAllOf(ctx, &kueue.LocalQueue{}, client.HasLabels{CleanupLabel}); err != nil {
		log.Error(err, "Deleting local queues")
	}

	if err := c.DeleteAllOf(ctx, &kueue.ClusterQueue{}, client.HasLabels{CleanupLabel}); err != nil {
		log.Error(err, "Deleting cluster queues")
	}

	if err := c.DeleteAllOf(ctx, &kueue.ResourceFlavor{}, client.HasLabels{CleanupLabel}); err != nil {
		log.Error(err, "Deleting resource flavor")
	}

	// Clean up TAS resources if present
	if err := c.DeleteAllOf(ctx, &kueue.Topology{}, client.HasLabels{CleanupLabel}); err != nil {
		log.Error(err, "Deleting topology")
	}

	if err := c.DeleteAllOf(ctx, &corev1.Node{}, client.HasLabels{CleanupLabel}); err != nil {
		log.Error(err, "Deleting nodes")
	}
}
