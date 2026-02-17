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
	"flag"
	"os"

	"go.uber.org/zap/zapcore"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	kueuepopulatorconfig "sigs.k8s.io/kueue/cmd/experimental/kueue-populator/pkg/config"
	"sigs.k8s.io/kueue/cmd/experimental/kueue-populator/pkg/controller"
)

var (
	scheme = runtime.NewScheme()
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kueue.AddToScheme(scheme))
}

func main() {
	var configFile string
	flag.StringVar(&configFile, "config", "",
		"The controller will load its initial configuration from this file. "+
			"Omit this flag to use the default configuration values.")
	opts := zap.Options{
		TimeEncoder: zapcore.RFC3339NanoTimeEncoder,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	if err := start(configFile); err != nil {
		os.Exit(1)
	}
}

func start(configFile string) error {
	log := ctrl.Log.WithName("setup")

	cfg, err := kueuepopulatorconfig.Load(configFile)
	if err != nil {
		log.Error(err, "Unable to load the configuration")
		return err
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: ":8081",
	})
	if err != nil {
		log.Error(err, "Unable to start manager")
		return err
	}

	reconcilerOpts := []controller.KueuePopulatorReconcilerOption{
		controller.WithLocalQueueName(cfg.LocalQueueName),
	}
	if cfg.ManagedJobsNamespaceSelector != nil {
		selector, err := metav1.LabelSelectorAsSelector(cfg.ManagedJobsNamespaceSelector)
		if err != nil {
			log.Error(err, "Unable to parse managed-jobs-namespace-selector")
			return err
		}
		reconcilerOpts = append(reconcilerOpts, controller.WithNamespaceSelector(selector))
	}

	if err = controller.NewKueuePopulatorReconciler(
		mgr.GetClient(),
		mgr.GetEventRecorderFor("kueue-populator"),
		reconcilerOpts...,
	).SetupWithManager(mgr); err != nil {
		log.Error(err, "Unable to create controller", "controller", "KueuePopulator")
		return err
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error(err, "Unable to set up health check")
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Error(err, "Unable to set up ready check")
		return err
	}

	log.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "Problem running manager")
		return err
	}
	return nil
}
