package main

import (
	"flag"
	"os"

	zaplog "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/config"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"

	"podtaintstolerations/controller"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kueue.AddToScheme(scheme))
	utilruntime.Must(configapi.AddToScheme(scheme))
}

func main() {
	var configFilePath string
	flag.StringVar(&configFilePath, "kueue-config", "",
		"Kueue system config file.")
	flag.StringVar(&controller.AdmissionTaintKey, "admission-taint-key", "kueue.x-k8s.io/kueue-admission",
		"The controller will add Pod tolerations for this taint key to implement admission.")

	opts := zap.Options{
		TimeEncoder: zapcore.RFC3339NanoTimeEncoder,
		ZapOpts:     []zaplog.Option{zaplog.AddCaller()},
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	_, cfg, err := config.Load(scheme, configFilePath)
	if err != nil {
		setupLog.Error(err, "Unable to load kueue configuration")
		os.Exit(1)
	}
	setupLog.Info("Using config file", "configFilePath", configFilePath)

	kubeConfig := ctrl.GetConfigOrDie()

	mgr, err := ctrl.NewManager(kubeConfig, ctrl.Options{
		Scheme:                  scheme,
		MetricsBindAddress:      ":8080",
		HealthProbeBindAddress:  ":8081",
		LeaderElection:          true,
		LeaderElectionID:        "ae7bde4d.podtaintstolerations.kueue.x-k8s.io",
		LeaderElectionNamespace: "kueue-system",
	})
	if err != nil {
		setupLog.Error(err, "Unable to start manager")
		os.Exit(1)
	}

	if err := controller.NewReconciler(
		mgr.GetClient(),
		mgr.GetEventRecorderFor("kueue-pod-taints-tolerations"),
		jobframework.WithWaitForPodsReady(waitForPodsReady(cfg)),
	).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Unable to create Pod (job) controller")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()

	if err := jobframework.SetupWorkloadOwnerIndex(ctx, mgr.GetFieldIndexer(),
		controller.GVK,
	); err != nil {
		setupLog.Error(err, "Setting up indexes")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "Could not run manager")
		os.Exit(1)
	}
}

func waitForPodsReady(cfg configapi.Configuration) bool {
	return cfg.WaitForPodsReady != nil && cfg.WaitForPodsReady.Enable
}
