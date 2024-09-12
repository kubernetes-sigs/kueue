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

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime/pprof"
	"syscall"

	zaplog "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	crconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/scheduler"
)

var (
	cpuprofile = flag.String("cpuprofile", "", "write cpu profile to `file`")

	metricsPort = flag.Int("metricsPort", 0, "metrics serving port")
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
	os.Exit(mainWithExitCode())
}

func mainWithExitCode() int {
	opts := zap.Options{
		TimeEncoder: zapcore.RFC3339NanoTimeEncoder,
		ZapOpts:     []zaplog.Option{zaplog.AddCaller()},
		Development: true,
		Level:       zaplog.NewAtomicLevelAt(zapcore.ErrorLevel),
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	log := zap.New(zap.UseFlagOptions(&opts))

	ctrl.SetLogger(log)
	log.Info("Start")

	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Error(err, "Could not create CPU profile")
		}
		defer f.Close() // error handling omitted for example
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Error(err, "Could not start CPU profile")
		}
		defer func() {
			log.Info("Stop CPU profile")
			pprof.StopCPUProfile()
		}()
	}

	kubeConfig, err := ctrl.GetConfig()
	if err != nil {
		log.Error(err, "get kubeconfig")
		return 1
	}

	// based on the default config
	kubeConfig.QPS = 50
	kubeConfig.Burst = 100
	log.Info("K8S Client", "Host", kubeConfig.Host, "qps", kubeConfig.QPS, "burst", kubeConfig.Burst)

	// based on the default config
	options := ctrl.Options{
		Scheme: scheme,
		Controller: crconfig.Controller{
			SkipNameValidation: ptr.To(true),
			GroupKindConcurrency: map[string]int{
				kueue.GroupVersion.WithKind("Workload").GroupKind().String():       5,
				kueue.GroupVersion.WithKind("LocalQueue").GroupKind().String():     1,
				kueue.GroupVersion.WithKind("ClusterQueue").GroupKind().String():   1,
				kueue.GroupVersion.WithKind("ResourceFlavor").GroupKind().String(): 1,
			},
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

	ctx, cancel := context.WithCancel(ctrl.LoggerInto(context.Background(), log))
	defer cancel()
	go func() {
		done := make(chan os.Signal, 2)
		signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)
		<-done
		log.Info("Cancel the manager's context")
		cancel()
	}()

	err = indexer.Setup(ctx, mgr.GetFieldIndexer())
	if err != nil {
		log.Error(err, "Indexer setup")
		return 1
	}

	cCache := cache.New(mgr.GetClient())
	queues := queue.NewManager(mgr.GetClient(), cCache)

	go queues.CleanUpOnContext(ctx)
	go cCache.CleanUpOnContext(ctx)

	if failedCtrl, err := core.SetupControllers(mgr, queues, cCache, &configapi.Configuration{}); err != nil {
		log.Error(err, "Unable to create controller", "controller", failedCtrl)
		return 1
	}

	sched := scheduler.New(
		queues,
		cCache,
		mgr.GetClient(),
		mgr.GetEventRecorderFor(constants.AdmissionName),
	)

	if err := mgr.Add(sched); err != nil {
		log.Error(err, "Unable to add scheduler to manager")
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
