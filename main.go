/*
Copyright 2021 The Kubernetes Authors.

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
	"bytes"
	"flag"
	"fmt"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	zaplog "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	configv1alpha1 "sigs.k8s.io/kueue/apis/config/v1alpha1"
	kueuev1alpha1 "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/workload/job"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/scheduler"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(schedulingv1.AddToScheme(scheme))

	utilruntime.Must(kueuev1alpha1.AddToScheme(scheme))
	utilruntime.Must(configv1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	var configFile string
	flag.StringVar(&configFile, "config", "",
		"The controller will load its initial configuration from this file. "+
			"Omit this flag to use the default configuration values. ")

	opts := zap.Options{
		TimeEncoder: zapcore.RFC3339NanoTimeEncoder,
		ZapOpts:     []zaplog.Option{zaplog.AddCaller(), zaplog.AddCallerSkip(-1)},
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	options := ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: ":8081",
		MetricsBindAddress:     ":8080",
		Port:                   9443,
		LeaderElectionID:       "c1f6bfd2.kueue.x-k8s.io",
	}
	var err error
	config := configv1alpha1.Configuration{}
	if configFile != "" {
		options, err = options.AndFrom(ctrl.ConfigFile().AtPath(configFile).OfKind(&config))
		if err != nil {
			setupLog.Error(err, "unable to load the config file")
			os.Exit(1)
		}

		cfgStr, err := encodeConfig(&config)
		if err != nil {
			setupLog.Error(err, "unable to encode config file")
			os.Exit(1)
		}
		setupLog.Info("Successfully loaded config file", "config", cfgStr)
	}
	metrics.Register()

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), options)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}
	if err := queue.SetupIndexes(mgr.GetFieldIndexer()); err != nil {
		setupLog.Error(err, "Unable to setup queue indexes")
	}
	if err := cache.SetupIndexes(mgr.GetFieldIndexer()); err != nil {
		setupLog.Error(err, "Unable to setup cache indexes")
	}

	queues := queue.NewManager(mgr.GetClient())
	cCache := cache.New(mgr.GetClient())
	if failedCtrl, err := core.SetupControllers(mgr, queues, cCache); err != nil {
		setupLog.Error(err, "Unable to create controller", "controller", failedCtrl)
	}
	if err = job.NewReconciler(mgr.GetScheme(),
		mgr.GetClient(),
		mgr.GetEventRecorderFor(constants.JobControllerName),
		job.WithManageJobsWithoutQueueName(config.ManageJobsWithoutQueueName),
	).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Job")
		os.Exit(1)
	}
	if err = (&kueuev1alpha1.Workload{}).SetupWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create webhook", "webhook", "Workload")
		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()
	go func() {
		queues.CleanUpOnContext(ctx)
	}()
	sched := scheduler.New(queues, cCache, mgr.GetClient(),
		mgr.GetEventRecorderFor(constants.ManagerName))
	go func() {
		sched.Start(ctx)
	}()
	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func encodeConfig(cfg *configv1alpha1.Configuration) (string, error) {
	codecs := serializer.NewCodecFactory(scheme)
	const mediaType = runtime.ContentTypeYAML
	info, ok := runtime.SerializerInfoForMediaType(codecs.SupportedMediaTypes(), mediaType)
	if !ok {
		return "", fmt.Errorf("unable to locate encoder -- %q is not a supported media type", mediaType)
	}

	encoder := codecs.EncoderForVersion(info.Serializer, configv1alpha1.GroupVersion)
	buf := new(bytes.Buffer)
	if err := encoder.Encode(cfg, buf); err != nil {
		return "", err
	}
	return buf.String(), nil
}
