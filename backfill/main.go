package main

import (
	"flag"
	"os"

	zaplog "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/backfill/controller"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kueue.AddToScheme(scheme))
}

func main() {
	opts := zap.Options{
		TimeEncoder: zapcore.RFC3339NanoTimeEncoder,
		ZapOpts:     []zaplog.Option{zaplog.AddCaller()},
	}
	opts.BindFlags(flag.CommandLine)
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	kubeConfig := ctrl.GetConfigOrDie()

	mgr, err := ctrl.NewManager(kubeConfig, ctrl.Options{
		Scheme: scheme,
	})
	if err != nil {
		setupLog.Error(err, "Unable to start manager")
		os.Exit(1)
	}

	if err = controller.NewReconciler(
		mgr.GetClient(),
		mgr.GetEventRecorderFor("backfill-kueue-controller"),
	).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Unable to create Pod (job) controller")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()

	if err := jobframework.SetupWorkloadOwnerIndex(ctx, mgr.GetFieldIndexer(), schema.GroupVersionKind{
		Group:   "",
		Version: "v1",
		Kind:    "Pod",
	}); err != nil {
		setupLog.Error(err, "Setting up indexes")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "Could not run manager")
		os.Exit(1)
	}
}
