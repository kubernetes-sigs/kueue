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
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	zaplog "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	crconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/test/performance/framework/controllers"
)

var (
	cpuprofile = flag.String("cpuprofile", "", "write cpu profile to `file`")
	memprofile = flag.String("memprofile", "", "write memory profile to file")

	metricsPort = flag.Int("metricsPort", 0, "metrics serving port")

	enableTAS = flag.Bool("enableTAS", false, "enable TAS controllers and indexers")
	enableDRA = flag.Bool("enableDRA", false, "enable the DRA device feasibility check and map the generated DeviceClass to quota")

	qps                 = flag.Float64("qps", 50, "Kubernetes client QPS")
	burst               = flag.Int("burst", 100, "Kubernetes client burst")
	workloadConcurrency = flag.Int("workloadConcurrency", 5, "maximum number of concurrent Workload reconciles, use default if non-positive")

	cpuProfileStartDelay = flag.Duration("cpuProfileStartDelay", 0, "delay before the first scheduled CPU profile")
	cpuProfileCount      = flag.Int("cpuProfileCount", 0, "number of scheduled CPU profiles to collect")
	cpuProfileDuration   = flag.Duration("cpuProfileDuration", 10*time.Second, "duration of each scheduled CPU profile")
	cpuProfileInterval   = flag.Duration("cpuProfileInterval", 0, "sleep after a scheduled CPU profile before starting the next one")
)

var (
	scheme = runtime.NewScheme()
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kueue.AddToScheme(scheme))
	utilruntime.Must(kueuealpha.AddToScheme(scheme))
	utilruntime.Must(configapi.AddToScheme(scheme))
}

func main() {
	initFlags()
	flag.Parse()
	os.Exit(run())
}

var logOptions = zap.Options{
	TimeEncoder: zapcore.RFC3339NanoTimeEncoder,
	ZapOpts:     []zaplog.Option{zaplog.AddCaller()},
	Development: true,
	Level:       zaplog.NewAtomicLevelAt(zapcore.ErrorLevel),
}

func initFlags() {
	logOptions.BindFlags(flag.CommandLine)
}

func run() int {
	log := zap.New(zap.UseFlagOptions(&logOptions))
	ctrl.SetLogger(log)
	if *enableTAS {
		log.Info("Start minimalkueue with TAS support")
	} else {
		log.Info("Start minimalkueue")
	}

	ctx, cancel := context.WithCancel(ctrl.LoggerInto(context.Background(), log))
	defer cancel()
	if *cpuprofile != "" {
		stopCPUProfiling, err := startCPUProfiling(ctx, log, *cpuprofile, *cpuProfileCount, *cpuProfileStartDelay, *cpuProfileDuration, *cpuProfileInterval)
		if err != nil {
			log.Error(err, "Could not start CPU profiling")
			return 1
		}
		defer stopCPUProfiling()
	}

	if *memprofile != "" {
		defer func() {
			log.Info("Write memory profile")

			f, err := os.Create(*memprofile)
			if err != nil {
				log.Error(err, "Could not create memory profile")
				return
			}
			defer f.Close()

			if err := pprof.WriteHeapProfile(f); err != nil {
				log.Error(err, "Could not write memory profile")
				return
			}
		}()
	}

	kubeConfig, err := ctrl.GetConfig()
	if err != nil {
		log.Error(err, "get kubeconfig")
		return 1
	}

	// based on the default config
	if *qps > 0 {
		kubeConfig.QPS = float32(*qps)
	}
	if *burst > 0 {
		kubeConfig.Burst = *burst
	}
	log.Info("K8S Client", "Host", kubeConfig.Host, "qps", kubeConfig.QPS, "burst", kubeConfig.Burst)

	// based on the default config
	effectiveWorkloadConcurrency := *workloadConcurrency
	if effectiveWorkloadConcurrency <= 0 {
		effectiveWorkloadConcurrency = 5
	}

	groupKindConcurrency := map[string]int{
		kueue.SchemeGroupVersion.WithKind("Workload").GroupKind().String():       effectiveWorkloadConcurrency,
		kueue.SchemeGroupVersion.WithKind("LocalQueue").GroupKind().String():     1,
		kueue.SchemeGroupVersion.WithKind("ClusterQueue").GroupKind().String():   1,
		kueue.SchemeGroupVersion.WithKind("ResourceFlavor").GroupKind().String(): 1,
	}
	if *enableTAS {
		groupKindConcurrency[kueue.SchemeGroupVersion.WithKind("Topology").GroupKind().String()] = 1
	}

	options := ctrl.Options{
		Scheme: scheme,
		Controller: crconfig.Controller{
			SkipNameValidation:   new(true),
			GroupKindConcurrency: groupKindConcurrency,
		},
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
	}

	if *metricsPort > 0 {
		options.Metrics.BindAddress = fmt.Sprintf(":%d", *metricsPort)
		metrics.Register()
	}

	mgr, err := ctrl.NewManager(kubeConfig, options)
	if err != nil {
		log.Error(err, "Unable to create manager")
		return 1
	}

	go func() {
		done := make(chan os.Signal, 2)
		signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)
		<-done
		log.Info("Cancel the manager's context")
		cancel()
	}()

	cfg := &configapi.Configuration{}
	if *enableDRA {
		if err := utilfeature.DefaultMutableFeatureGate.SetFromMap(map[string]bool{string(features.KueueDRADeviceFeasibility): true}); err != nil {
			log.Error(err, "Unable to enable the DRA device feasibility check")
			return 1
		}
		cfg.Resources = &configapi.Resources{DeviceClassMappings: controllers.DRADeviceClassMappings()}
	}

	if err := controllers.Setup(ctx, mgr, cfg, *enableTAS); err != nil {
		log.Error(err, "Unable to set up controllers and scheduler")
		return 1
	}

	log.Info("Starting manager")
	if err := mgr.Start(ctx); err != nil {
		log.Error(err, "Could not run manager")
		return 1
	}

	log.Info("Done")
	return 0
}

func startCPUProfiling(ctx context.Context, log logr.Logger, profilePath string, count int, startDelay, duration, interval time.Duration) (func(), error) {
	if count > 0 {
		if startDelay < 0 || duration <= 0 || interval < 0 {
			return nil, errors.New("scheduled CPU profile delays must be non-negative and duration must be positive")
		}
		profileCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			runCPUProfileSchedule(profileCtx, log, profilePath, count, startDelay, duration, interval)
		}()
		return func() { cancel(); <-done }, nil
	}
	f, err := os.Create(profilePath)
	if err != nil {
		return nil, fmt.Errorf("create CPU profile: %w", err)
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("start CPU profile: %w", err)
	}
	return func() {
		pprof.StopCPUProfile()
		_ = f.Close()
	}, nil
}

func runCPUProfileSchedule(ctx context.Context, log logr.Logger, profilePath string, count int, startDelay, duration, interval time.Duration) {
	if err := waitForDuration(ctx, startDelay); err != nil {
		return
	}
	ext := filepath.Ext(profilePath)
	base := strings.TrimSuffix(profilePath, ext)
	for i := 1; i <= count; i++ {
		path := fmt.Sprintf("%s.%03d%s", base, i, ext)
		f, err := os.Create(path)
		if err != nil {
			log.Error(err, "Could not create scheduled CPU profile", "path", path)
			return
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			_ = f.Close()
			log.Error(err, "Could not start scheduled CPU profile", "path", path)
			return
		}
		completed := waitForDuration(ctx, duration) == nil
		pprof.StopCPUProfile()
		_ = f.Close()
		if !completed || i == count || waitForDuration(ctx, interval) != nil {
			return
		}
	}
}

func waitForDuration(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
