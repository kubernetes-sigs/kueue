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

package manager

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/manager"
	"sigs.k8s.io/kueue/pkg/util/cert"
	"sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta1"
)

var _ = ginkgo.Describe("SetupIndexes", func() {
	var (
		managerCtx context.Context
		cancel     context.CancelFunc
		mgr        ctrl.Manager
	)

	ginkgo.BeforeEach(func() {
		var err error
		managerCtx, cancel = context.WithCancel(context.Background())

		ginkgo.By("Cleaning up resources before test")
		_ = k8sClient.DeleteAllOf(ctx, &kueue.LocalQueue{})
		_ = k8sClient.DeleteAllOf(ctx, &kueue.ClusterQueue{})
		_ = k8sClient.DeleteAllOf(ctx, &kueue.Workload{})

		time.Sleep(time.Millisecond * 100)

		mgrCfg := manager.NewConfig()
		gomega.Expect(mgrCfg.Apply(filepath.Join("../../../../config", "components", "manager", "controller_manager_config.yaml"))).To(gomega.Succeed())
		mgrCfg.Options.LeaderElection = false
		mgrCfg.Options.HealthProbeBindAddress = ":0"
		mgrCfg.Options.Metrics.BindAddress = ":0"

		mgr, err = ctrl.NewManager(cfg, mgrCfg.Options)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		k8sClient = mgr.GetClient()

		ginkgo.By("Setting up indexes")
		err = mgrCfg.SetupIndexes(managerCtx, mgr)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By("Starting the manager")
		go func() {
			defer ginkgo.GinkgoRecover()
			gomega.Expect(mgr.Start(managerCtx)).To(gomega.Succeed())
		}()
	})

	ginkgo.AfterEach(func() {
		cancel()
	})

	ginkgo.It("registers and queries indexes", func() {
		_ = k8sClient.DeleteAllOf(ctx, &kueue.LocalQueue{}, client.InNamespace("default"))
		_ = k8sClient.DeleteAllOf(ctx, &kueue.Workload{}, client.InNamespace("default"))
		_ = k8sClient.DeleteAllOf(ctx, &corev1.LimitRange{}, client.InNamespace("default"))
		_ = k8sClient.DeleteAllOf(ctx, &corev1.Pod{}, client.InNamespace("default"))
		_ = k8sClient.DeleteAllOf(ctx, &kueue.ResourceFlavor{})

		time.Sleep(time.Millisecond * 200)

		lq := utiltestingapi.MakeLocalQueue("q1", "default").ClusterQueue("cq-a").Obj()
		gomega.Expect(k8sClient.Create(ctx, lq)).To(gomega.Succeed())

		gomega.Eventually(func() bool {
			var createdLQ kueue.LocalQueue
			err := k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), &createdLQ)
			return err == nil
		}, "10s", "100ms").Should(gomega.BeTrue())

		wl := utiltestingapi.MakeWorkload("wl1", "default").
			Queue("q1").
			ControllerReference(schema.GroupVersion{Group: "batch", Version: "v1"}.WithKind("Job"), "job-a", "uid-a")
		wlObj := wl.Obj()
		gomega.Expect(k8sClient.Create(ctx, wlObj)).To(gomega.Succeed())

		var gotWL kueue.Workload
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(mgr.GetAPIReader().Get(ctx, client.ObjectKeyFromObject(wlObj), &gotWL)).To(gomega.Succeed())
		}, "10s", "100ms").Should(gomega.Succeed())
		gotWL.Status.Admission = utiltestingapi.MakeAdmission("cq-a").Obj()
		gotWL.Status.Conditions = []metav1.Condition{{
			Type: kueue.WorkloadQuotaReserved, Status: metav1.ConditionTrue, Reason: "ByTest", Message: "Admitted by test", LastTransitionTime: metav1.Now(),
		}}
		gomega.Expect(k8sClient.Status().Update(ctx, &gotWL)).To(gomega.Succeed())

		lr := testing.MakeLimitRange("lr1", "default").Obj()
		gomega.Expect(k8sClient.Create(ctx, lr)).To(gomega.Succeed())

		// check core indexes
		gomega.Eventually(func() bool {
			var lqs kueue.LocalQueueList
			err := k8sClient.List(ctx, &lqs, client.MatchingFields{indexer.QueueClusterQueueKey: "cq-a"})
			if err != nil || len(lqs.Items) == 0 {
				return false
			}
			for _, lq := range lqs.Items {
				if string(lq.Spec.ClusterQueue) == "cq-a" {
					return true
				}
			}
			return false
		}, "10s", "100ms").Should(gomega.BeTrue())

		gomega.Eventually(func() bool {
			var wls kueue.WorkloadList
			_ = k8sClient.List(ctx, &wls, client.MatchingFields{indexer.WorkloadQueueKey: "q1"})
			if len(wls.Items) == 0 {
				return false
			}
			for _, wl := range wls.Items {
				if wl.Spec.QueueName == "q1" {
					return true
				}
			}
			return false
		}, "10s", "100ms").Should(gomega.BeTrue())

		gomega.Eventually(func() bool {
			var wls kueue.WorkloadList
			_ = k8sClient.List(ctx, &wls, client.MatchingFields{indexer.WorkloadClusterQueueKey: "cq-a"})
			if len(wls.Items) == 0 {
				return false
			}
			for _, wl := range wls.Items {
				if wl.Status.Admission != nil && string(wl.Status.Admission.ClusterQueue) == "cq-a" {
					return true
				}
			}
			return false
		}, "10s", "100ms").Should(gomega.BeTrue())

		gomega.Eventually(func() bool {
			var wls kueue.WorkloadList
			_ = k8sClient.List(ctx, &wls, client.MatchingFields{indexer.WorkloadQuotaReservedKey: string(metav1.ConditionTrue)})
			if len(wls.Items) == 0 {
				return false
			}
			for _, wl := range wls.Items {
				for _, cond := range wl.Status.Conditions {
					if cond.Type == kueue.WorkloadQuotaReserved && cond.Status == metav1.ConditionTrue {
						return true
					}
				}
			}
			return false
		}, "10s", "100ms").Should(gomega.BeTrue())

		gomega.Eventually(func() bool {
			gvk := schema.GroupVersion{Group: "batch", Version: "v1"}.WithKind("Job")
			var wls kueue.WorkloadList
			matcher := jobframework.OwnerReferenceIndexFieldMatcher(gvk, "job-a")
			_ = k8sClient.List(ctx, &wls, matcher)
			if len(wls.Items) == 0 {
				return false
			}
			for _, wl := range wls.Items {
				for _, ownerRef := range wl.OwnerReferences {
					if ownerRef.Kind == "Job" && ownerRef.Name == "job-a" {
						return true
					}
				}
			}
			return false
		}, "10s", "100ms").Should(gomega.BeTrue())

		gomega.Eventually(func() bool {
			var lrs corev1.LimitRangeList
			_ = k8sClient.List(ctx, &lrs, client.MatchingFields{indexer.LimitRangeHasContainerType: "true"})
			if len(lrs.Items) == 0 {
				return false
			}
			for _, lr := range lrs.Items {
				for _, limit := range lr.Spec.Limits {
					if limit.Type == corev1.LimitTypeContainer {
						return true
					}
				}
			}
			return false
		}, "10s", "100ms").Should(gomega.BeTrue())

		// check tas indexes
		if features.Enabled(features.TopologyAwareScheduling) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:   "default",
					Name:        "p1",
					Annotations: map[string]string{kueue.WorkloadAnnotation: "wl1"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "c", Image: "busybox", Command: []string{"sh", "-c", "echo"}},
					},
				},
			}
			gomega.Expect(k8sClient.Create(ctx, pod)).To(gomega.Succeed())

			gomega.Eventually(func() bool {
				var pods corev1.PodList
				_ = k8sClient.List(ctx, &pods, client.MatchingFields{tasindexer.WorkloadNameKey: "wl1"})
				if len(pods.Items) == 0 {
					return false
				}
				for _, pod := range pods.Items {
					if pod.Annotations != nil && pod.Annotations[kueue.WorkloadAnnotation] == "wl1" {
						return true
					}
				}
				return false
			}, "10s", "100ms").Should(gomega.BeTrue())

			rf := utiltestingapi.MakeResourceFlavor("rf-a").TopologyName("topo.a").NodeLabel("topo.a/level", "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, rf)).To(gomega.Succeed())

			gomega.Eventually(func() bool {
				var rfs kueue.ResourceFlavorList
				_ = k8sClient.List(ctx, &rfs, client.MatchingFields{tasindexer.ResourceFlavorTopologyNameKey: "topo.a"})
				if len(rfs.Items) == 0 {
					return false
				}
				for _, rf := range rfs.Items {
					if rf.Spec.TopologyName != nil && *rf.Spec.TopologyName == "topo.a" {
						return true
					}
				}
				return false
			}, "10s", "100ms").Should(gomega.BeTrue())
		}
	})
})

var _ = ginkgo.Describe("SetupControllers", func() {
	var (
		mgr       ctrl.Manager
		mgrCfg    *manager.Config
		cl        client.Client
		rawClient client.Client // direct (non-cached) client for test reads
		cCache    *schdcache.Cache
		queues    *qcache.Manager
		ctx       context.Context
		cancel    context.CancelFunc
		certDir   string
	)

	ginkgo.BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		realCfg := filepath.Join("../../../../config", "components", "manager", "controller_manager_config.yaml")
		mgrCfg = manager.NewConfig()

		var err error
		gomega.Expect(mgrCfg.Apply(realCfg)).To(gomega.Succeed())
		mgrCfg.Options.LeaderElection = false

		mgrCfg.Options.HealthProbeBindAddress = ":0"
		mgrCfg.Options.Metrics.BindAddress = ":0"

		// Create a unique cert directory per test run to avoid races with watchers
		certDir, err = os.MkdirTemp("", "kueue-webhook-certs-*")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		certPEM := `-----BEGIN CERTIFICATE-----
MIIDCTCCAfGgAwIBAgIUey/SyCXL8fiZgV2kZrqfGkW6df8wDQYJKoZIhvcNAQEL
BQAwFDESMBAGA1UEAwwJbG9jYWxob3N0MB4XDTI1MDgyNDE1NDYxNVoXDTI2MDgy
NDE1NDYxNVowFDESMBAGA1UEAwwJbG9jYWxob3N0MIIBIjANBgkqhkiG9w0BAQEF
AAOCAQ8AMIIBCgKCAQEAmwkMkAERs2AggkoEFMevBkvHWolKbUr30Z10OHj/YppD
8n6th4yKwUVeyO2iXEDrdlYgIkd8NfCH2nVHgo4SIaDa9nkShL2APbocHYSjkYBN
CGfv4UPS+Nou1whM1Y0827apvPK5JdmKjxNuflxQdsrDQJb1KEHr4MrKE64VlfLI
ChO+AQHQmrXG4aAH7LuspLoR3/EwvXvdfWQMMEn+n+9OeY7auV6E50xLFpq0a/S9
x/G7DVxXtdLBc0KVuEwUIKvdiunDYHHJhv58+vrfVpJVDHi+iVykN6ec3vD7DaTB
gx1YpgsQIQTB1SL1n+sjOfDswlmd1isLrOlgfMhG0wIDAQABo1MwUTAdBgNVHQ4E
FgQU5k54o3GoEfWzeJakCKi8cG4y7LkwHwYDVR0jBBgwFoAU5k54o3GoEfWzeJak
CKi8cG4y7LkwDwYDVR0TAQH/BAUwAwEB/zANBgkqhkiG9w0BAQsFAAOCAQEATEt7
MLf9SzNvGcYWM9jles8xeI2mr2TrUEBoyhhDA9JN/t3MKjTRsLTu28PgsGJM4xc0
HnMjET/hwvL5+ekKJtmaEkkqZiNTxZjPfO6gw+ZCbmcL6yjqeEa/9D5onZ6ipFDx
CUF/pCA3CSAC62TfCrA4w8Oaenzn4ONPUT4fIWCdlrOyLnTdZutmjqoIyMFcL9Gk
3NZZ0QmT2CEK9ICoG9i0oAJaNeXa0WoXXxaqKdeNYHZ6hVh40YWpkszRwB7m9Wl1
Sce3XsvvYg8HibuB/GnRSiNGqmTU34+0dm6TjioWp1BsB1K7BCM0hmh2niqwx4eI
It6mu4Gr4j5fdXMETQ==
-----END CERTIFICATE-----`

		keyPEM := `-----BEGIN PRIVATE KEY-----
MIIEvwIBADANBgkqhkiG9w0BAQEFAASCBKkwggSlAgEAAoIBAQCbCQyQARGzYCCC
SgQUx68GS8daiUptSvfRnXQ4eP9imkPyfq2HjIrBRV7I7aJcQOt2ViAiR3w18Ifa
dUeCjhIhoNr2eRKEvYA9uhwdhKORgE0IZ+/hQ9L42i7XCEzVjTzbtqm88rkl2YqP
E25+XFB2ysNAlvUoQevgysoTrhWV8sgKE74BAdCatcbhoAfsu6ykuhHf8TC9e919
ZAwwSf6f7055jtq5XoTnTEsWmrRr9L3H8bsNXFe10sFzQpW4TBQgq92K6cNgccmG
/nz6+t9WklUMeL6JXKQ3p5ze8PsNpMGDHVimCxAhBMHVIvWf6yM58OzCWZ3WKwus
6WB8yEbTAgMBAAECggEALfiT8gtvHTpOyXN7HFJNstc7iLwXBqtpKo2+zZQLXkiS
B1DK0du5tS+FuJzGPQa/CzrkkmWSDkiBcCTAjJTmCXSyGM2z0QqEAUmzVoljGxzp
OqnfNnOvFj1UEE0Uw2n69seGM1Hh1rhX3q8LX4quDVt4ZCmfDk3lzKU1IHrJScna
SBN1ucPN91EjSLcRHGskczB86VPbTO6JxO8khk0kCr2MC/dGO8C7YGwlngh3l4gp
LRmlUo4H2YuO33wIjbbz1tpaW0iSqpxszsTGvckpJ++fn9ZJOittHLdFN8iZdg1T
LmCJRTaSRtmzEAIVNFveid54RaSHShW2LujbyUK6SQKBgQDXT8tDcqB+fY23Oknt
k5y/ZiIag4ny0UD9EvIUVpieQKosjTH/bh4tdy+4bOfxQr4JchytI7bD1lIV/rjV
nwse/mwtGgV8bY3VTG2JZGaIJj3bJ+ZIJDb+uxuxBh39WA1MaBCMbUdeSTr9yewb
YD1jQ+3dR0fpZ4uYXBEXuQ1uHQKBgQC4VT77uBSTX6egBgyICYX8CLBIfE/+4JmV
qc+L41Bczb+HrnuTE57HyttS/j6XKWAB0XRURQAI1StTlm9Fbj4Gl1hfoGVq1LV1
lvUMy3jjSY9haRXQdMMeTGs5wQ/Ii7qqw98oBIwBBeilQuIoUnMulQCfHA0zTDa3
xSchpqY1rwKBgQCgyBJGZJOawVERMTLBeUhE2RTAbdeWflIkaYBiVaQUEL/DExDx
6B4a33TAKHsveyKD1TW6yP+S0Dlt+U+3HdPlKiJHr7XHC9wtGqx1O4chRkVMoUfi
OUDkCX8NOz8rzxPnKZKp+nSf4NlvaNiqPLy6oqA+bBs0HUFt3dpZt7NitQKBgQCR
kPAeBG5rOzy6iExZGXwvXgUoGNNraZ6fq+v0glwyDWDVGxsHOJVJHY856QEwikIA
7ZE6AwtV7lE6vy+72qUsu1PUoGu2g6eQ5tc5dW1PwAV0XXIWnj5/rMV4ZFe8fWu5
8thFV+Hf5PSlnT3Prdy7ynslKxfZjLQhR5XxYxMajQKBgQChurJU0GYpQg/F0w1V
K3JGHzD7WxuPuEtMRwAyKUKRZL6Soc6kd3IOp47RplVgQfzfu5xetwADfs76iIo1
Gtq1E6IVqH81gg7TCJZtHXwN1lp85B/dKsqOBK/Jvfifzo4tVGL00fSLS5q4XgFL
WBR6QR1OiJANLk5gid3x34imLg==
-----END PRIVATE KEY-----`

		gomega.Expect(os.WriteFile(filepath.Join(certDir, "tls.crt"), []byte(certPEM), 0644)).To(gomega.Succeed())
		gomega.Expect(os.WriteFile(filepath.Join(certDir, "tls.key"), []byte(keyPEM), 0644)).To(gomega.Succeed())

		*mgrCfg.Apiconf.InternalCertManagement.Enable = false

		gomega.Expect(features.SetEnable(features.MultiKueue, true)).To(gomega.Succeed())
		gomega.Expect(features.SetEnable(features.TopologyAwareScheduling, true)).To(gomega.Succeed())

		skipValidation := true
		mgrCfg.Options.Controller.SkipNameValidation = &skipValidation

		// Allocate a free TCP port for the webhook server to avoid :9443 conflicts.
		// controller-runtime overwrites Port<=0 with 9443, so we must choose a real free port.
		l, err := net.Listen("tcp", "127.0.0.1:0")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		port := l.Addr().(*net.TCPAddr).Port
		_ = l.Close()

		mgrCfg.Options.WebhookServer = webhook.NewServer(webhook.Options{Port: port, CertDir: certDir})
		mgr, err = ctrl.NewManager(cfg, mgrCfg.Options)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		cl = mgr.GetClient()

		gomega.Expect(mgrCfg.SetupIndexes(ctx, mgr)).To(gomega.Succeed())

		certsReady := make(chan struct{})
		if mgrCfg.Apiconf.InternalCertManagement != nil && *mgrCfg.Apiconf.InternalCertManagement.Enable {
			err = cert.ManageCerts(mgr, mgrCfg.Apiconf, certsReady)
			gomega.Expect(err).NotTo(gomega.HaveOccurred(), "Unable to set up cert rotation")
		} else {
			close(certsReady)
		}

		cacheOptions := []schdcache.Option{schdcache.WithPodsReadyTracking(mgrCfg.BlockForPodsReady())}
		queueOptions := []qcache.Option{qcache.WithPodsReadyRequeuingTimestamp(mgrCfg.PodsReadyRequeuingTimestamp())}
		if mgrCfg.Apiconf.Resources != nil && len(mgrCfg.Apiconf.Resources.ExcludeResourcePrefixes) > 0 {
			cacheOptions = append(cacheOptions, schdcache.WithExcludedResourcePrefixes(mgrCfg.Apiconf.Resources.ExcludeResourcePrefixes))
			queueOptions = append(queueOptions, qcache.WithExcludedResourcePrefixes(mgrCfg.Apiconf.Resources.ExcludeResourcePrefixes))
		}
		if features.Enabled(features.ConfigurableResourceTransformations) && mgrCfg.Apiconf.Resources != nil && len(mgrCfg.Apiconf.Resources.Transformations) > 0 {
			cacheOptions = append(cacheOptions, schdcache.WithResourceTransformations(mgrCfg.Apiconf.Resources.Transformations))
			queueOptions = append(queueOptions, qcache.WithResourceTransformations(mgrCfg.Apiconf.Resources.Transformations))
		}
		if mgrCfg.Apiconf.FairSharing != nil {
			cacheOptions = append(cacheOptions, schdcache.WithFairSharing(mgrCfg.Apiconf.FairSharing.Enable))
		}
		if mgrCfg.Apiconf.AdmissionFairSharing != nil {
			queueOptions = append(queueOptions, qcache.WithAdmissionFairSharing(mgrCfg.Apiconf.AdmissionFairSharing))
			cacheOptions = append(cacheOptions, schdcache.WithAdmissionFairSharing(mgrCfg.Apiconf.AdmissionFairSharing))
		}

		cCache = schdcache.New(mgr.GetClient(), cacheOptions...)
		queues = qcache.NewManager(mgr.GetClient(), cCache, queueOptions...)

		serverVersionFetcher, err := mgrCfg.SetupServerVersionFetcher(mgr, cfg)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		gomega.Expect(mgrCfg.SetupControllers(ctx, mgr, cCache, queues, certsReady, serverVersionFetcher)).To(gomega.Succeed())

		_ = cl.DeleteAllOf(ctx, &kueue.LocalQueue{})
		_ = cl.DeleteAllOf(ctx, &kueue.Workload{})
		_ = cl.DeleteAllOf(ctx, &kueue.ResourceFlavor{})
		_ = cl.DeleteAllOf(ctx, &kueue.ClusterQueue{})
		_ = cl.DeleteAllOf(ctx, &kueue.Cohort{})
		_ = cl.DeleteAllOf(ctx, &kueue.AdmissionCheck{})
		_ = cl.DeleteAllOf(ctx, &corev1.LimitRange{})
		_ = cl.DeleteAllOf(ctx, &corev1.Pod{})
		_ = cl.DeleteAllOf(ctx, &batchv1.Job{})

		time.Sleep(100 * time.Millisecond)

		go queues.CleanUpOnContext(ctx)
		go cCache.CleanUpOnContext(ctx)

		go func() {
			if err := mgr.Start(ctx); err != nil {
				mgrCfg.SetupLog.Error(err, "Could not run manager")
			}
		}()

		gomega.Expect(mgr.GetCache().WaitForCacheSync(ctx)).To(gomega.BeTrue())
		cl = mgr.GetClient()

		// create a raw (uncached) client to avoid Eventually races with cache for newly created objects
		var err2 error
		rawClient, err2 = client.New(cfg, client.Options{Scheme: mgr.GetScheme()})
		gomega.Expect(err2).NotTo(gomega.HaveOccurred())
	})

	ginkgo.AfterEach(func() {
		if cancel != nil {
			cancel()
		}
		time.Sleep(200 * time.Millisecond)
		if certDir != "" {
			_ = os.RemoveAll(certDir)
		}
		if cl != nil {
			_ = cl.DeleteAllOf(ctx, &kueue.ResourceFlavor{})
			_ = cl.DeleteAllOf(ctx, &kueue.ClusterQueue{})
			_ = cl.DeleteAllOf(ctx, &kueue.AdmissionCheck{})
			_ = cl.DeleteAllOf(ctx, &kueue.Cohort{})
			_ = cl.DeleteAllOf(ctx, &kueue.LocalQueue{}, client.InNamespace("default"))
			_ = cl.DeleteAllOf(ctx, &kueue.Workload{}, client.InNamespace("default"))
		}
	})

	ginkgo.Context("Core Controllers", func() {
		ginkgo.It("starts ResourceFlavor controller", func() {
			rf := utiltestingapi.MakeResourceFlavor("rf1").Obj()
			gomega.Expect(cl.Create(ctx, rf)).To(gomega.Succeed())

			gomega.Eventually(func() bool {
				var gotRF kueue.ResourceFlavor
				if err := cl.Get(ctx, client.ObjectKeyFromObject(rf), &gotRF); err != nil {
					return false
				}

				if gotRF.Name != rf.Name {
					return false
				}
				if gotRF.CreationTimestamp.IsZero() {
					return false
				}
				return true
			}, "10s", "100ms").Should(gomega.BeTrue())
		})

		ginkgo.It("starts LocalQueue controller", func() {
			lq := utiltestingapi.MakeLocalQueue("q1", "default").ClusterQueue("cq1").Obj()
			gomega.Expect(rawClient.Create(ctx, lq)).To(gomega.Succeed())

			gomega.Eventually(func() bool {
				var gotLQ kueue.LocalQueue
				if err := rawClient.Get(ctx, client.ObjectKeyFromObject(lq), &gotLQ); err != nil {
					return false
				}
				if gotLQ.Name != lq.Name {
					return false
				}
				if gotLQ.CreationTimestamp.IsZero() {
					return false
				}
				return true
			}, "30s", "500ms").Should(gomega.BeTrue())
		})

		ginkgo.It("starts Workload controller", func() {
			wl := utiltestingapi.MakeWorkload("wl1", "default").Queue("q1").Obj()
			gomega.Expect(rawClient.Create(ctx, wl)).To(gomega.Succeed())

			gomega.Eventually(func() bool {
				var gotWL kueue.Workload
				if err := rawClient.Get(ctx, client.ObjectKeyFromObject(wl), &gotWL); err != nil {
					return false
				}

				if gotWL.Name != wl.Name {
					return false
				}
				if gotWL.CreationTimestamp.IsZero() {
					return false
				}
				return true
			}, "30s", "500ms").Should(gomega.BeTrue())
		})

		ginkgo.It("starts AdmissionCheck controller", func() {
			ac := utiltestingapi.MakeAdmissionCheck("ac1")
			gomega.Expect(rawClient.Create(ctx, ac.Obj())).To(gomega.Succeed())

			gomega.Eventually(func() bool {
				var gotAC kueue.AdmissionCheck
				if err := rawClient.Get(ctx, client.ObjectKeyFromObject(ac.Obj()), &gotAC); err != nil {
					return false
				}
				if gotAC.Name != ac.Name {
					return false
				}
				if gotAC.CreationTimestamp.IsZero() {
					return false
				}
				return true
			}, "20s", "100ms").Should(gomega.BeTrue())
		})

		ginkgo.It("starts Cohort controller", func() {
			cohort := utiltestingapi.MakeCohort("cohort1").Obj()
			gomega.Expect(rawClient.Create(ctx, cohort)).To(gomega.Succeed())

			gomega.Eventually(func() bool {
				var gotCohort kueue.Cohort
				if err := rawClient.Get(ctx, client.ObjectKeyFromObject(cohort), &gotCohort); err != nil {
					return false
				}
				if gotCohort.Name != cohort.Name {
					return false
				}
				if gotCohort.CreationTimestamp.IsZero() {
					return false
				}
				return true
			}, "30s", "500ms").Should(gomega.BeTrue())
		})

		ginkgo.It("starts ClusterQueue controller", func() {
			cq := utiltestingapi.MakeClusterQueue("cq1").Obj()
			gomega.Expect(rawClient.Create(ctx, cq)).To(gomega.Succeed())

			gomega.Eventually(func() bool {
				var gotCQ kueue.ClusterQueue
				if err := rawClient.Get(ctx, client.ObjectKeyFromObject(cq), &gotCQ); err != nil {
					return false
				}
				if gotCQ.Name != cq.Name {
					return false
				}
				if gotCQ.CreationTimestamp.IsZero() {
					return false
				}
				return true
			}, "30s", "500ms").Should(gomega.BeTrue())
		})
	})

	ginkgo.Context("Feature-Specific Controllers", func() {
		ginkgo.It("starts TAS controllers when enabled", func() {
			if !features.Enabled(features.TopologyAwareScheduling) {
				ginkgo.Skip("TopologyAwareScheduling not enabled")
			}

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "node1"},
				Spec:       corev1.NodeSpec{Unschedulable: false},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
					},
				},
			}
			gomega.Expect(rawClient.Create(ctx, node)).To(gomega.Succeed())

			gomega.Eventually(func() bool {
				var gotNode corev1.Node
				if err := rawClient.Get(ctx, client.ObjectKeyFromObject(node), &gotNode); err != nil {
					return false
				}

				if gotNode.Name != node.Name {
					return false
				}
				if gotNode.CreationTimestamp.IsZero() {
					return false
				}
				return true
			}, "30s", "500ms").Should(gomega.BeTrue())
		})

		ginkgo.It("starts MultiKueue controllers when enabled", func() {
			if !features.Enabled(features.MultiKueue) {
				ginkgo.Skip("MultiKueue not enabled")
			}

			namespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "kueue-system",
				},
			}
			gomega.Expect(cl.Create(ctx, namespace)).To(gomega.Succeed())

			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kubeconfig-secret",
					Namespace: "kueue-system",
				},
				Data: map[string][]byte{
					"kubeconfig": []byte("fake-kubeconfig-data"),
				},
			}
			gomega.Expect(cl.Create(ctx, secret)).To(gomega.Succeed())

			mkc := utiltestingapi.MakeMultiKueueCluster("c1").KubeConfig(kueue.SecretLocationType, "kubeconfig-secret").Obj()
			gomega.Expect(cl.Create(ctx, mkc)).To(gomega.Succeed())

			mkcfg := utiltestingapi.MakeMultiKueueConfig("cfg1").Clusters("c1").Obj()
			gomega.Expect(cl.Create(ctx, mkcfg)).To(gomega.Succeed())
		})

		ginkgo.It("starts job framework controllers", func() {
			lq := utiltestingapi.MakeLocalQueue("test-queue", "default").ClusterQueue("test-cq").Obj()
			gomega.Expect(rawClient.Create(ctx, lq)).To(gomega.Succeed())

			job := &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-job",
					Namespace: "default",
					Labels: map[string]string{
						"kueue.x-k8s.io/queue-name": "test-queue",
					},
				},
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers:    []corev1.Container{{Name: "test", Image: "busybox"}},
							RestartPolicy: corev1.RestartPolicyNever,
						},
					},
				},
			}
			gomega.Expect(rawClient.Create(ctx, job)).To(gomega.Succeed())

			gomega.Eventually(func() bool {
				var workloads kueue.WorkloadList
				if err := rawClient.List(ctx, &workloads, client.InNamespace("default")); err != nil {
					return false
				}
				for _, wl := range workloads.Items {
					if wl.Spec.QueueName == "test-queue" {
						for _, ownerRef := range wl.OwnerReferences {
							if ownerRef.Kind == "Job" && ownerRef.Name == "test-job" {
								return true
							}
						}
					}
				}
				return false
			}, "30s", "500ms").Should(gomega.BeTrue())

			gomega.Eventually(func() bool {
				var gotJob batchv1.Job
				if err := rawClient.Get(ctx, client.ObjectKeyFromObject(job), &gotJob); err != nil {
					return false
				}
				return gotJob.Spec.Suspend != nil && *gotJob.Spec.Suspend == true
			}, "30s", "500ms").Should(gomega.BeTrue())
		})
	})

	ginkgo.Context("Webhook Server", func() {
		ginkgo.It("configures webhook server", func() {
			gomega.Eventually(func() bool {
				return mgr.GetWebhookServer() != nil
			}, "5s", "100ms").Should(gomega.BeTrue())
		})
	})
})

var _ = ginkgo.Describe("SetupScheduler", func() {
	var (
		mgr    ctrl.Manager
		mgrCfg *manager.Config
		cCache *schdcache.Cache
		queues *qcache.Manager
	)

	ginkgo.BeforeEach(func() {
		// TODO use minimal config
		realCfg := filepath.Join("../../../../config", "components", "manager", "controller_manager_config.yaml")
		mgrCfg = manager.NewConfig()
		gomega.Expect(mgrCfg.Apply(realCfg)).To(gomega.Succeed())
		mgrCfg.Options.LeaderElection = false
		mgrCfg.Options.HealthProbeBindAddress = ":0"
		mgrCfg.Options.Metrics.BindAddress = ":0"

		var err error
		mgr, err = ctrl.NewManager(cfg, mgrCfg.Options)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		cacheOptions := []schdcache.Option{schdcache.WithPodsReadyTracking(mgrCfg.BlockForPodsReady())}
		queueOptions := []qcache.Option{qcache.WithPodsReadyRequeuingTimestamp(mgrCfg.PodsReadyRequeuingTimestamp())}
		if mgrCfg.Apiconf.Resources != nil && len(mgrCfg.Apiconf.Resources.ExcludeResourcePrefixes) > 0 {
			cacheOptions = append(cacheOptions, schdcache.WithExcludedResourcePrefixes(mgrCfg.Apiconf.Resources.ExcludeResourcePrefixes))
			queueOptions = append(queueOptions, qcache.WithExcludedResourcePrefixes(mgrCfg.Apiconf.Resources.ExcludeResourcePrefixes))
		}
		if features.Enabled(features.ConfigurableResourceTransformations) && mgrCfg.Apiconf.Resources != nil && len(mgrCfg.Apiconf.Resources.Transformations) > 0 {
			cacheOptions = append(cacheOptions, schdcache.WithResourceTransformations(mgrCfg.Apiconf.Resources.Transformations))
			queueOptions = append(queueOptions, qcache.WithResourceTransformations(mgrCfg.Apiconf.Resources.Transformations))
		}
		if mgrCfg.Apiconf.FairSharing != nil {
			cacheOptions = append(cacheOptions, schdcache.WithFairSharing(mgrCfg.Apiconf.FairSharing.Enable))
		}
		if mgrCfg.Apiconf.AdmissionFairSharing != nil {
			queueOptions = append(queueOptions, qcache.WithAdmissionFairSharing(mgrCfg.Apiconf.AdmissionFairSharing))
			cacheOptions = append(cacheOptions, schdcache.WithAdmissionFairSharing(mgrCfg.Apiconf.AdmissionFairSharing))
		}

		cCache = schdcache.New(mgr.GetClient(), cacheOptions...)
		queues = qcache.NewManager(mgr.GetClient(), cCache, queueOptions...)
	})

	ginkgo.Context("Basic Scheduler Setup", func() {
		ginkgo.It("successfully sets up scheduler with valid arguments", func() {
			err := mgrCfg.SetupScheduler(mgr, cCache, queues)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(mgr).NotTo(gomega.BeNil())
		})

		ginkgo.It("panics with nil manager", func() {
			gomega.Expect(func() {
				_ = mgrCfg.SetupScheduler(nil, cCache, queues)
			}).To(gomega.Panic())
		})

		ginkgo.It("handles nil cache", func() {
			err := mgrCfg.SetupScheduler(mgr, nil, queues)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("handles nil queues", func() {
			err := mgrCfg.SetupScheduler(mgr, cCache, nil)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("panics with all nil arguments", func() {
			gomega.Expect(func() {
				_ = mgrCfg.SetupScheduler(nil, nil, nil)
			}).To(gomega.Panic())
		})

		ginkgo.It("handles scheduler with fair sharing enabled", func() {
			mgrCfg.Apiconf.FairSharing = &configapi.FairSharing{Enable: true}

			err := mgrCfg.SetupScheduler(mgr, cCache, queues)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("handles scheduler with admission fair sharing enabled", func() {
			mgrCfg.Apiconf.AdmissionFairSharing = &configapi.AdmissionFairSharing{
				UsageHalfLifeTime: metav1.Duration{Duration: 15 * time.Minute},
			}

			err := mgrCfg.SetupScheduler(mgr, cCache, queues)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("handles scheduler with pods ready requeuing enabled", func() {
			mgrCfg.Apiconf.WaitForPodsReady = &configapi.WaitForPodsReady{Enable: true}

			err := mgrCfg.SetupScheduler(mgr, cCache, queues)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("handles scheduler with multiple configuration options", func() {
			mgrCfg.Apiconf.FairSharing = &configapi.FairSharing{Enable: true}
			mgrCfg.Apiconf.AdmissionFairSharing = &configapi.AdmissionFairSharing{
				UsageHalfLifeTime: metav1.Duration{Duration: 15 * time.Minute},
			}
			mgrCfg.Apiconf.WaitForPodsReady = &configapi.WaitForPodsReady{Enable: true}

			err := mgrCfg.SetupScheduler(mgr, cCache, queues)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})
	})
})
