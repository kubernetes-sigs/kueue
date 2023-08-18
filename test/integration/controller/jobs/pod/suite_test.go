package pod

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	"sigs.k8s.io/kueue/pkg/webhooks"
	"sigs.k8s.io/kueue/test/integration/framework"
	//+kubebuilder:scaffold:imports
)

var (
	cfg                  *rest.Config
	k8sClient            client.Client
	serverVersionFetcher *kubeversion.ServerVersionFetcher
	ctx                  context.Context
	fwk                  *framework.Framework
	crdPath              = filepath.Join("..", "..", "..", "..", "..", "config", "components", "crd", "bases")
	webhookPath          = filepath.Join("..", "..", "..", "..", "..", "config", "components", "webhook")
)

func TestAPIs(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)

	ginkgo.RunSpecs(t,
		"Pod Controller Suite",
	)
}

func managerSetup(opts ...jobframework.Option) framework.ManagerSetup {
	return func(mgr manager.Manager, ctx context.Context) {
		err := pod.SetupIndexes(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		podReconciler := pod.NewReconciler(
			mgr.GetClient(),
			mgr.GetEventRecorderFor(constants.JobControllerName),
			opts...)
		err = podReconciler.SetupWithManager(mgr)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		// Job reconciler is enabled for "pod parent managed by queue" tests
		jobReconciler := job.NewReconciler(
			mgr.GetClient(),
			mgr.GetEventRecorderFor(constants.JobControllerName),
			opts...)
		err = jobReconciler.SetupWithManager(mgr)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		cCache := cache.New(mgr.GetClient())
		queues := queue.NewManager(mgr.GetClient(), cCache)

		failedCtrl, err := core.SetupControllers(mgr, queues, cCache, &config.Configuration{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "controller", failedCtrl)

		err = pod.SetupWebhook(mgr, opts...)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		failedWebhook, err := webhooks.Setup(mgr)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)
	}
}

// hasKueueSchedulingGate returns true, if pod contains 'kueue.x-k8s.io/admission'
// scheduling gate
func hasKueueSchedulingGate(p *corev1.Pod) bool {
	for i := range p.Spec.SchedulingGates {
		if p.Spec.SchedulingGates[i].Name == "kueue.x-k8s.io/admission" {
			return true
		}
	}

	return false
}

// hasManagedLabel returns true, if pod contains 'kueue.x-k8s.io/managed' label
func hasManagedLabel(p *corev1.Pod) bool {
	if l, ok := p.Labels["kueue.x-k8s.io/managed"]; ok && l == "true" {
		return true
	}
	return false
}
