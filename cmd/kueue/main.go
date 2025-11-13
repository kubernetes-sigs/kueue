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
	"os"

	zaplog "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/manager"
	"sigs.k8s.io/kueue/pkg/version"
	"sigs.k8s.io/kueue/pkg/visibility"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	// Ensure linking of the job controllers.
	_ "sigs.k8s.io/kueue/pkg/controller/jobs"
)

func main() {
	var configFile string
	flag.StringVar(&configFile, "config", "",
		"The controller will load its initial configuration from this file. "+
			"Omit this flag to use the default configuration values. ")

	var featureGates string
	flag.StringVar(&featureGates, "feature-gates", "", "A set of key=value pairs that describe feature gates for alpha/experimental features.")

	opts := zap.Options{
		TimeEncoder: zapcore.RFC3339NanoTimeEncoder,
		ZapOpts:     []zaplog.Option{zaplog.AddCaller()},
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	ctx := ctrl.SetupSignalHandler()
	if err := run(ctx, configFile, featureGates); err != nil {
		os.Exit(1)
	}
}

func run(ctx context.Context, configFile, featureGates string) error {
	setupLog := ctrl.Log.WithName("setup")
	setupLog.Info("Initializing", "gitVersion", version.GitVersion, "gitCommit", version.GitCommit, "buildDate", version.BuildDate)
	features.LogFeatureGates(setupLog)

	mgrSetup, err := manager.SetupManager(ctx, configFile, featureGates)
	if err != nil {
		setupLog.Error(err, "Unable to setup manager")
		return err
	}

	// Cert won't be ready until manager starts, so start a goroutine here which
	// will block until the cert is ready before setting up the controllers.
	go func() {
		if err := mgrSetup.Config.SetupControllers(ctx, mgrSetup.Mgr, mgrSetup.CCache, mgrSetup.Queues, mgrSetup.CertsReady, mgrSetup.ServerVersionFetcher); err != nil {
			setupLog.Error(err, "Unable to setup controllers")
			os.Exit(1)
		}
	}()

	go mgrSetup.Queues.CleanUpOnContext(ctx)
	go mgrSetup.CCache.CleanUpOnContext(ctx)

	if features.Enabled(features.VisibilityOnDemand) {
		go func() {
			internalCertEnabled := mgrSetup.Config.Apiconf.InternalCertManagement != nil && *mgrSetup.Config.Apiconf.InternalCertManagement.Enable
			if err := visibility.CreateAndStartVisibilityServer(ctx, mgrSetup.Queues, internalCertEnabled, mgrSetup.Mgr.GetConfig()); err != nil {
				setupLog.Error(err, "Unable to create and start visibility server")
				os.Exit(1)
			}
		}()
	}

	setupLog.Info("Starting manager")
	if err := mgrSetup.Mgr.Start(ctx); err != nil {
		setupLog.Error(err, "Could not run manager")
		return err
	}
	return nil
}
