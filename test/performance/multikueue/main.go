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
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"syscall"

	zaplog "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/yaml"
)

var (
	configPath = flag.String(
		"config",
		"test/performance/multikueue/configs/baseline.yaml",
		"benchmark configuration file",
	)
	crdsPath = flag.String(
		"crds",
		"config/components/crd/bases",
		"path containing Kueue CRDs",
	)
	outputDirectory = flag.String(
		"o",
		"artifacts/run-performance-multikueue",
		"directory for benchmark artifacts",
	)
	workloadCountOverride   = flag.Int("workloads", 0, "override the configured workload count")
	workerClustersOverride  = flag.Int("workerClusters", 0, "override the configured worker cluster count")
	creationWorkersOverride = flag.Int("creationWorkers", 0, "override the configured creation worker count")
)

const runnerLogName = "runner.log"

// Controller logs go to runnerLogName rather than being discarded, so that a run failing after
// several minutes leaves something to diagnose, and rather than to stderr, so that expected
// reconcile conflicts do not bury the progress output. Raise the level with --zap-log-level.
var logOptions = zap.Options{
	TimeEncoder: zapcore.RFC3339NanoTimeEncoder,
	ZapOpts:     []zaplog.Option{zaplog.AddCaller()},
	Level:       zaplog.NewAtomicLevelAt(zapcore.ErrorLevel),
}

func main() {
	logOptions.BindFlags(flag.CommandLine)
	flag.Parse()

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "MultiKueue performance benchmark failed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	if *workloadCountOverride > 0 {
		cfg.WorkloadCount = *workloadCountOverride
	}
	if *workerClustersOverride > 0 {
		cfg.WorkerClusters = *workerClustersOverride
	}
	if *creationWorkersOverride > 0 {
		cfg.CreationWorkers = *creationWorkersOverride
	}
	if err := cfg.validate(); err != nil {
		return err
	}

	if err := os.MkdirAll(*outputDirectory, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	logFile, err := os.Create(filepath.Join(*outputDirectory, runnerLogName))
	if err != nil {
		return fmt.Errorf("create runner log: %w", err)
	}
	defer logFile.Close()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&logOptions), zap.WriteTo(logFile)))

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	workers := make([]*benchmarkCluster, 0, cfg.WorkerClusters)
	defer func() {
		for _, worker := range slices.Backward(workers) {
			if err := worker.stop(); err != nil {
				fmt.Fprintf(os.Stderr, "Cleanup warning: %v\n", err)
			}
		}
	}()

	for i := range cfg.WorkerClusters {
		name := workerName(i)
		fmt.Printf("Starting %s control plane\n", name)
		worker, err := startBenchmarkCluster(ctx, name, *crdsPath, setupCoreControllers)
		if err != nil {
			return err
		}
		workers = append(workers, worker)
	}

	fmt.Printf(
		"Starting manager control plane; remote clients at client-go's default %.0f QPS, burst %d per REST client\n",
		float32(rest.DefaultQPS),
		rest.DefaultBurst,
	)
	managerCluster, err := startBenchmarkCluster(
		ctx,
		"manager",
		*crdsPath,
		setupManagerControllers(configNamespaceName),
	)
	if err != nil {
		return err
	}
	defer func() {
		if err := managerCluster.stop(); err != nil {
			fmt.Fprintf(os.Stderr, "Cleanup warning: %v\n", err)
		}
	}()

	fmt.Println("Configuring one manager and", cfg.WorkerClusters, "worker clusters")
	if err := setupBenchmarkTopology(ctx, managerCluster, workers, cfg); err != nil {
		return err
	}

	benchmarkCtx, benchmarkCancel := context.WithTimeout(ctx, cfg.Timeout.Duration)
	defer benchmarkCancel()
	fmt.Println("Creating", cfg.WorkloadCount, "workloads")
	summary, err := runBenchmark(benchmarkCtx, managerCluster, cfg)
	if err != nil {
		return err
	}

	data, err := yaml.Marshal(summary)
	if err != nil {
		return fmt.Errorf("encode summary: %w", err)
	}
	summaryPath := filepath.Join(*outputDirectory, "summary.yaml")
	if err := os.WriteFile(summaryPath, data, 0o644); err != nil {
		return fmt.Errorf("write summary: %w", err)
	}

	fmt.Printf(
		"Admitted %d workloads at %.2f workloads/s; P95 admission latency: %dms\n",
		cfg.WorkloadCount,
		summary.ThroughputPerSecond,
		summary.Latencies.AdmissionMs.P95Ms,
	)
	fmt.Println("Summary:", summaryPath)
	return nil
}
