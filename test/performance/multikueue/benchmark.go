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
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/version"
	"sigs.k8s.io/kueue/test/performance/multikueue/report"
)

const (
	configNamespaceName          = "kueue-system"
	benchmarkNamespaceName       = "multikueue-performance"
	resourceFlavorName           = "default"
	clusterQueueName             = "multikueue-performance"
	localQueueName               = "multikueue-performance"
	multiKueueConfigName         = "multikueue-performance"
	multiKueueAdmissionCheck     = "multikueue-performance"
	benchmarkRunLabel            = "kueue.x-k8s.io/multikueue-performance-run"
	benchmarkCreatedAtAnnotation = "kueue.x-k8s.io/multikueue-performance-created-at"
)

type workloadTiming struct {
	createdAt       time.Time
	quotaReservedAt time.Time
	dispatchReadyAt time.Time
	admittedAt      time.Time
	cluster         string
}

type observationCollector struct {
	workloads map[types.NamespacedName]*workloadTiming
	admitted  int
}

func newObservationCollector() *observationCollector {
	return &observationCollector{
		workloads: make(map[types.NamespacedName]*workloadTiming),
	}
}

func (c *observationCollector) observe(wl *kueue.Workload, observedAt time.Time) error {
	key := client.ObjectKeyFromObject(wl)
	timing := c.workloads[key]
	if timing == nil {
		timing = &workloadTiming{}
		c.workloads[key] = timing
	}
	if timing.createdAt.IsZero() {
		// Falling back to CreationTimestamp would silently drop the measurement to the
		// one-second granularity this annotation exists to avoid, so refuse to guess.
		value := wl.Annotations[benchmarkCreatedAtAnnotation]
		if value == "" {
			return fmt.Errorf("workload %s has no %s annotation", key, benchmarkCreatedAtAnnotation)
		}
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return fmt.Errorf("parse %s annotation of workload %s: %w", benchmarkCreatedAtAnnotation, key, err)
		}
		timing.createdAt = parsed
	}
	if timing.quotaReservedAt.IsZero() {
		if condition := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved); condition != nil && condition.Status == metav1.ConditionTrue {
			timing.quotaReservedAt = observedAt
		}
	}
	if timing.dispatchReadyAt.IsZero() {
		for i := range wl.Status.AdmissionChecks {
			check := &wl.Status.AdmissionChecks[i]
			if check.Name == multiKueueAdmissionCheck && check.State == kueue.CheckStateReady {
				timing.dispatchReadyAt = observedAt
				break
			}
		}
	}
	if timing.admittedAt.IsZero() {
		if condition := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadAdmitted); condition != nil && condition.Status == metav1.ConditionTrue {
			timing.admittedAt = observedAt
			c.admitted++
		}
	}
	if timing.cluster == "" && wl.Status.ClusterName != nil {
		timing.cluster = *wl.Status.ClusterName
	}
	return nil
}

func (c *observationCollector) admittedCount() int {
	return c.admitted
}

func (c *observationCollector) summarize(cfg benchmarkConfig, generationDuration time.Duration, watchGaps int) (report.Summary, error) {
	if c.admittedCount() != cfg.WorkloadCount {
		return report.Summary{}, fmt.Errorf("observed %d admitted workloads, want %d", c.admittedCount(), cfg.WorkloadCount)
	}

	quotaLatencies := make([]time.Duration, 0, cfg.WorkloadCount)
	admissionLatencies := make([]time.Duration, 0, cfg.WorkloadCount)
	distribution := make(map[string]int, cfg.WorkerClusters)
	for i := range cfg.WorkerClusters {
		distribution[workerName(i)] = 0
	}
	var firstCreated, lastCreated, lastAdmitted time.Time

	for key, timing := range c.workloads {
		if timing.createdAt.IsZero() || timing.quotaReservedAt.IsZero() || timing.dispatchReadyAt.IsZero() || timing.admittedAt.IsZero() {
			return report.Summary{}, fmt.Errorf("incomplete timing data for workload %s", key)
		}
		if timing.cluster == "" {
			return report.Summary{}, fmt.Errorf("missing worker assignment for workload %s", key)
		}
		if firstCreated.IsZero() || timing.createdAt.Before(firstCreated) {
			firstCreated = timing.createdAt
		}
		if timing.createdAt.After(lastCreated) {
			lastCreated = timing.createdAt
		}
		if timing.admittedAt.After(lastAdmitted) {
			lastAdmitted = timing.admittedAt
		}
		quotaLatencies = append(quotaLatencies, timing.quotaReservedAt.Sub(timing.createdAt))
		admissionLatencies = append(admissionLatencies, timing.admittedAt.Sub(timing.createdAt))
		distribution[timing.cluster]++
	}

	totalDuration := lastAdmitted.Sub(firstCreated)
	if totalDuration <= 0 {
		return report.Summary{}, fmt.Errorf("invalid total duration %s", totalDuration)
	}
	drainDuration := lastAdmitted.Sub(lastCreated)

	build := version.Get()
	return report.Summary{
		Build: report.Build{
			GitVersion: build.GitVersion,
			GitCommit:  build.GitCommit,
			GoVersion:  build.GoVersion,
			Platform:   build.Platform,
		},
		Scenario: report.Scenario{
			WorkloadCount:       cfg.WorkloadCount,
			WorkerClusters:      cfg.WorkerClusters,
			CreationWorkers:     cfg.CreationWorkers,
			CPURequest:          cfg.CPURequest,
			Dispatcher:          benchmarkDispatcherName,
			WorkloadConcurrency: workloadConcurrency,
			GCInterval:          benchmarkGCInterval.String(),
			WorkerLostTimeout:   benchmarkWorkerLostTimeout.String(),
			EventsBatchPeriod:   benchmarkEventsBatchPeriod.String(),
			LocalClientQPS:      apiQPS,
			LocalClientBurst:    apiBurst,
			RemoteClientQPS:     cfg.RemoteClientQPS,
			RemoteClientBurst:   int(cfg.RemoteClientBurst),
		},
		Timing: report.Timing{
			GenerationMs: generationDuration.Milliseconds(),
			TotalMs:      totalDuration.Milliseconds(),
			DrainMs:      drainDuration.Milliseconds(),
		},
		ThroughputPerSecond: float64(cfg.WorkloadCount) / totalDuration.Seconds(),
		Latencies: report.Latencies{
			QuotaReservationMs: report.SummarizeDurations(quotaLatencies),
			AdmissionMs:        report.SummarizeDurations(admissionLatencies),
		},
		WatchGaps:          watchGaps,
		WorkerDistribution: distribution,
	}, nil
}

func setupBenchmarkTopology(ctx context.Context, managerCluster *benchmarkCluster, workers []*benchmarkCluster, cfg benchmarkConfig) error {
	quota := resource.MustParse(cfg.CPURequest)
	quota.Mul(int64(cfg.WorkloadCount))
	quotaString := quota.String()

	for _, worker := range workers {
		if err := createNamespace(ctx, worker.client, benchmarkNamespaceName); err != nil {
			return fmt.Errorf("setup %s namespace: %w", worker.name, err)
		}
		if err := createQueue(ctx, worker.client, quotaString, false); err != nil {
			return fmt.Errorf("setup %s queues: %w", worker.name, err)
		}
	}

	if err := createNamespace(ctx, managerCluster.client, configNamespaceName); err != nil {
		return fmt.Errorf("setup manager config namespace: %w", err)
	}
	if err := createNamespace(ctx, managerCluster.client, benchmarkNamespaceName); err != nil {
		return fmt.Errorf("setup manager benchmark namespace: %w", err)
	}

	clusterNames := make([]string, 0, len(workers))
	for _, worker := range workers {
		kubeconfig, err := utiltesting.RestConfigToKubeConfig(worker.config)
		if err != nil {
			return fmt.Errorf("serialize %s kubeconfig: %w", worker.name, err)
		}
		secretName := worker.name
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: configNamespaceName,
			},
			Data: map[string][]byte{
				kueue.MultiKueueConfigSecretKey: kubeconfig,
			},
		}
		if err := managerCluster.client.Create(ctx, secret); err != nil {
			return fmt.Errorf("create %s kubeconfig secret: %w", worker.name, err)
		}
		multiKueueCluster := utiltestingapi.MakeMultiKueueCluster(worker.name).
			KubeConfig(kueue.SecretLocationType, secretName).
			Obj()
		if err := managerCluster.client.Create(ctx, multiKueueCluster); err != nil {
			return fmt.Errorf("create MultiKueueCluster %s: %w", worker.name, err)
		}
		clusterNames = append(clusterNames, worker.name)
	}

	multiKueueConfig := utiltestingapi.MakeMultiKueueConfig(multiKueueConfigName).
		Clusters(clusterNames...).
		Obj()
	if err := managerCluster.client.Create(ctx, multiKueueConfig); err != nil {
		return fmt.Errorf("create MultiKueueConfig: %w", err)
	}
	admissionCheck := utiltestingapi.MakeAdmissionCheck(multiKueueAdmissionCheck).
		ControllerName(kueue.MultiKueueControllerName).
		Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", multiKueueConfigName).
		Obj()
	if err := managerCluster.client.Create(ctx, admissionCheck); err != nil {
		return fmt.Errorf("create AdmissionCheck: %w", err)
	}

	for _, worker := range workers {
		if err := waitForActive(ctx, managerCluster.client, client.ObjectKey{Name: worker.name}, &kueue.MultiKueueCluster{}, kueue.MultiKueueClusterActive); err != nil {
			return fmt.Errorf("wait for MultiKueueCluster %s: %w", worker.name, err)
		}
	}
	if err := waitForActive(ctx, managerCluster.client, client.ObjectKey{Name: multiKueueAdmissionCheck}, &kueue.AdmissionCheck{}, kueue.AdmissionCheckActive); err != nil {
		return fmt.Errorf("wait for MultiKueue AdmissionCheck: %w", err)
	}
	if err := createQueue(ctx, managerCluster.client, quotaString, true); err != nil {
		return fmt.Errorf("setup manager queues: %w", err)
	}
	return nil
}

func createNamespace(ctx context.Context, c client.Client, name string) error {
	return c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})
}

func createQueue(ctx context.Context, c client.Client, quota string, withAdmissionCheck bool) error {
	flavor := utiltestingapi.MakeResourceFlavor(resourceFlavorName).Obj()
	if err := c.Create(ctx, flavor); err != nil {
		return err
	}
	clusterQueue := utiltestingapi.MakeClusterQueue(clusterQueueName).
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas(resourceFlavorName).
				Resource(corev1.ResourceCPU, quota).
				Obj(),
		)
	if withAdmissionCheck {
		clusterQueue.AdmissionChecks(multiKueueAdmissionCheck)
	}
	if err := c.Create(ctx, clusterQueue.Obj()); err != nil {
		return err
	}
	if err := waitForActive(ctx, c, client.ObjectKey{Name: clusterQueueName}, &kueue.ClusterQueue{}, kueue.ClusterQueueActive); err != nil {
		return err
	}
	localQueue := utiltestingapi.MakeLocalQueue(localQueueName, benchmarkNamespaceName).
		ClusterQueue(clusterQueueName).
		Obj()
	if err := c.Create(ctx, localQueue); err != nil {
		return err
	}
	return waitForActive(
		ctx,
		c,
		client.ObjectKey{Name: localQueueName, Namespace: benchmarkNamespaceName},
		&kueue.LocalQueue{},
		kueue.LocalQueueActive,
	)
}

func waitForActive(ctx context.Context, c client.Client, key client.ObjectKey, object client.Object, conditionType string) error {
	return wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, time.Minute, true, func(ctx context.Context) (bool, error) {
		if err := c.Get(ctx, key, object); err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		var conditions []metav1.Condition
		switch typed := object.(type) {
		case *kueue.MultiKueueCluster:
			conditions = typed.Status.Conditions
		case *kueue.AdmissionCheck:
			conditions = typed.Status.Conditions
		case *kueue.ClusterQueue:
			conditions = typed.Status.Conditions
		case *kueue.LocalQueue:
			conditions = typed.Status.Conditions
		default:
			return false, fmt.Errorf("unsupported condition object %T", object)
		}
		return apimeta.IsStatusConditionTrue(conditions, conditionType), nil
	})
}

func runBenchmark(ctx context.Context, managerCluster *benchmarkCluster, cfg benchmarkConfig) (report.Summary, error) {
	runCtx, cancelRun := context.WithCancel(ctx)
	var runWG sync.WaitGroup
	defer func() {
		cancelRun()
		runWG.Wait()
	}()

	runID := strconv.FormatInt(time.Now().UnixNano(), 10)
	collector := newObservationCollector()

	tracker := &watchTracker{}
	observed := make(chan timedWorkload, watchBufferPerWorkload*cfg.WorkloadCount)
	watcher, resourceVersion, err := openWorkloadWatch(runCtx, managerCluster.client, runID)
	if err != nil {
		return report.Summary{}, fmt.Errorf("watch benchmark workloads: %w", err)
	}
	watchDone := make(chan error, 1)
	runWG.Go(func() {
		watchDone <- receiveWorkloads(
			runCtx,
			managerCluster.client,
			runID,
			watcher,
			resourceVersion,
			tracker,
			observed,
		)
	})

	generationDone := make(chan generationResult, 1)
	runWG.Go(func() {
		started := time.Now()
		err := generateWorkloads(runCtx, managerCluster.client, cfg, runID)
		generationDone <- generationResult{
			duration: time.Since(started),
			err:      err,
		}
	})

	var generationDuration time.Duration
	reportedMilestone := 0
	for collector.admittedCount() < cfg.WorkloadCount {
		select {
		case result := <-generationDone:
			generationDone = nil
			generationDuration = result.duration
			if result.err != nil {
				return report.Summary{}, result.err
			}
		case event, ok := <-observed:
			if !ok {
				if err := <-watchDone; err != nil {
					return report.Summary{}, err
				}
				return report.Summary{}, fmt.Errorf(
					"workload watch ended after %d/%d admissions",
					collector.admittedCount(),
					cfg.WorkloadCount,
				)
			}
			if err := collector.observe(event.workload, event.observedAt); err != nil {
				return report.Summary{}, err
			}
			milestone := collector.admittedCount() * 10 / cfg.WorkloadCount
			if milestone > reportedMilestone {
				reportedMilestone = milestone
				fmt.Printf("Admission progress: %d/%d\n", collector.admittedCount(), cfg.WorkloadCount)
			}
		case <-runCtx.Done():
			return report.Summary{}, fmt.Errorf(
				"wait for admissions: %w (admitted %d/%d)",
				runCtx.Err(),
				collector.admittedCount(),
				cfg.WorkloadCount,
			)
		}
	}

	if generationDone != nil {
		result := <-generationDone
		generationDuration = result.duration
		if result.err != nil {
			return report.Summary{}, result.err
		}
	}
	return collector.summarize(cfg, generationDuration, int(tracker.gaps.Load()))
}

// watchBufferPerWorkload sizes the handover buffer between receiving watch events and processing
// them, as a multiple of the run's workload count. A Workload goes through a bounded number of
// transitions, so a run's whole event stream fits and the receiving loop never blocks. Config
// validation bounds the resulting allocation.
const watchBufferPerWorkload = 8

type timedWorkload struct {
	workload   *kueue.Workload
	observedAt time.Time
}

// watchTracker counts re-established watches for report.Summary.WatchGaps so disturbed timing and
// throughput observations remain visible in the report.
type watchTracker struct {
	gaps atomic.Int64
}

// receiveWorkloads forwards this run's Workloads as the watch delivers them, re-establishing the
// watch if it ends early. The initial watch is opened before generation starts so no Workload can
// be created before the observation stream is ready. This function keeps every other step off the
// receive path: the apiserver terminates a watcher it cannot deliver events to, and a timestamp
// taken after the processing loop had queued the event would inflate measured control-plane latency.
func receiveWorkloads(
	ctx context.Context,
	c client.WithWatch,
	runID string,
	watcher watch.Interface,
	resourceVersion string,
	tracker *watchTracker,
	out chan<- timedWorkload,
) error {
	defer close(out)
	// Deferred through a closure rather than directly, because resuming replaces watcher and it is
	// the last one that has to be stopped.
	defer func() {
		watcher.Stop()
	}()

	for {
		for event := range watcher.ResultChan() {
			// An error ends the run: it carries a status rather than an object, and the usual one
			// is that the resource version to resume from has expired, which no retry recovers.
			if event.Type == watch.Error {
				return fmt.Errorf("workload watch error: %v", event.Object)
			}
			if event.Type != watch.Added && event.Type != watch.Modified {
				continue
			}
			workload, ok := event.Object.(*kueue.Workload)
			if !ok {
				continue
			}
			resourceVersion = workload.ResourceVersion
			select {
			case out <- timedWorkload{workload: workload, observedAt: time.Now()}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		if err := ctx.Err(); err != nil {
			return err
		}
		watcher.Stop()
		tracker.gaps.Add(1)
		resumedWatcher, err := watchWorkloadsFrom(ctx, c, runID, resourceVersion)
		if err != nil {
			return fmt.Errorf("resume workload watch at resource version %s: %w", resourceVersion, err)
		}
		watcher = resumedWatcher
	}
}

// openWorkloadWatch starts watching this run's Workloads from the resource version at which they
// do not yet exist, so no transition can slip through between the list and the watch.
func openWorkloadWatch(ctx context.Context, c client.WithWatch, runID string) (watch.Interface, string, error) {
	list := &kueue.WorkloadList{}
	if err := c.List(
		ctx,
		list,
		client.InNamespace(benchmarkNamespaceName),
		client.MatchingLabels{benchmarkRunLabel: runID},
	); err != nil {
		return nil, "", err
	}
	if len(list.Items) > 0 {
		return nil, "", fmt.Errorf("run %s already has %d workloads", runID, len(list.Items))
	}
	watcher, err := watchWorkloadsFrom(ctx, c, runID, list.ResourceVersion)
	if err != nil {
		return nil, "", err
	}
	return watcher, list.ResourceVersion, nil
}

func watchWorkloadsFrom(ctx context.Context, c client.WithWatch, runID, resourceVersion string) (watch.Interface, error) {
	return c.Watch(
		ctx,
		&kueue.WorkloadList{},
		client.InNamespace(benchmarkNamespaceName),
		client.MatchingLabels{benchmarkRunLabel: runID},
		&client.ListOptions{Raw: &metav1.ListOptions{ResourceVersion: resourceVersion}},
	)
}

type generationResult struct {
	duration time.Duration
	err      error
}

func generateWorkloads(ctx context.Context, c client.Client, cfg benchmarkConfig, runID string) error {
	group, groupCtx := errgroup.WithContext(ctx)
	var next atomic.Int64

	for range cfg.CreationWorkers {
		group.Go(func() error {
			for {
				index := int(next.Add(1) - 1)
				if index >= cfg.WorkloadCount {
					return nil
				}
				if err := createWorkload(groupCtx, c, cfg, runID, index); err != nil {
					return err
				}
			}
		})
	}
	if err := group.Wait(); err != nil {
		return fmt.Errorf("generate workloads: %w", err)
	}
	return nil
}

// createWorkload creates the Job and its Workload directly instead of letting the Job reconciler
// derive one. No job framework controller runs here, but the Job still has to exist: the MultiKueue
// workload reconciler resolves the adapter that mirrors objects to the workers from the Workload's
// owner.
func createWorkload(ctx context.Context, c client.Client, cfg benchmarkConfig, runID string, index int) error {
	name := fmt.Sprintf("workload-%06d", index)
	job := testingjob.MakeJob(name, benchmarkNamespaceName).
		Queue(localQueueName).
		RequestAndLimit(corev1.ResourceCPU, cfg.CPURequest).
		ManagedBy(kueue.MultiKueueControllerName).
		Label(benchmarkRunLabel, runID).
		Obj()
	if err := c.Create(ctx, job); err != nil {
		return fmt.Errorf("create Job %s: %w", name, err)
	}

	workload := utiltestingapi.MakeWorkload(name, benchmarkNamespaceName).
		Queue(localQueueName).
		Request(corev1.ResourceCPU, cfg.CPURequest).
		ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), job.Name, string(job.UID)).
		Label(benchmarkRunLabel, runID).
		Annotation(benchmarkCreatedAtAnnotation, time.Now().Format(time.RFC3339Nano)).
		Obj()
	if err := c.Create(ctx, workload); err != nil {
		return fmt.Errorf("create Workload %s: %w", name, err)
	}
	return nil
}
