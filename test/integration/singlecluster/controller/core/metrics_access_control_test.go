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

package core

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/cert"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/config"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Metrics access control", ginkgo.Label("area:core", "feature:metrics"), func() {
	var scraper *http.Client

	startServer := func(mode *configapi.MetricsAccessControlMode) string {
		ginkgo.GinkgoHelper()
		certPEM, keyPEM, err := cert.GenerateSelfSignedCertKey("localhost", []net.IP{net.ParseIP("127.0.0.1")}, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		servingCert, err := tls.X509KeyPair(certPEM, keyPEM)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		roots := x509.NewCertPool()
		gomega.Expect(roots.AppendCertsFromPEM(certPEM)).To(gomega.BeTrue())
		// Use a separate client so envtest's admin credentials are never sent to the metrics listener.
		scraper = &http.Client{
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}},
			Timeout:   5 * time.Second,
		}
		ginkgo.DeferCleanup(scraper.CloseIdleConnections)
		apiClient, err := rest.HTTPClientFor(cfg)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		// Let the OS allocate a port to avoid conflicts with concurrently running suites.
		options := config.MetricsServerOptions(ctrl.Log, configapi.ControllerMetrics{AccessControlMode: mode, BindAddress: "127.0.0.1:0"})
		options.TLSOpts = []func(*tls.Config){func(tlsConfig *tls.Config) {
			tlsConfig.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &servingCert, nil }
		}}
		server, err := metricsserver.NewServer(options, cfg, apiClient)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		serverCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		ginkgo.DeferCleanup(func() {
			cancel()
			gomega.Eventually(done, behavioral.LongTimeout).Should(gomega.Receive(gomega.Succeed()))
		})
		go func() { done <- server.Start(serverCtx) }()
		addressProvider, ok := server.(interface{ GetBindAddr() string })
		gomega.Expect(ok).To(gomega.BeTrue())
		gomega.Eventually(addressProvider.GetBindAddr, behavioral.Timeout, behavioral.Interval).ShouldNot(gomega.BeEmpty())
		return "https://" + addressProvider.GetBindAddr() + "/metrics"
	}

	expectResponse := func(endpoint, token string, status int) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			response, err := scraper.Do(req)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(response.StatusCode).To(gomega.Equal(status), "response: %s", body)
			if status == http.StatusOK {
				g.Expect(string(body)).To(gomega.ContainSubstring("go_gc_duration_seconds"))
			}
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}

	ginkgo.It("serves metrics over verified HTTPS without a token when None is enabled", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MetricsAccessControl, true)
		endpoint := startServer(new(configapi.MetricsAccessControlModeNone))
		expectResponse(endpoint, "", http.StatusOK)
	})

	ginkgo.It("retains delegated authentication and authorization with the feature gate disabled", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MetricsAccessControl, false)
		endpoint := startServer(nil)
		expectResponse(endpoint, "", http.StatusUnauthorized)

		ginkgo.By("rejecting a valid ServiceAccount token without metrics permission")
		ns := utiltesting.MakeNamespaceWrapper("").GenerateName("metrics-access-control-").Obj()
		behavioral.MustCreate(ctx, k8sClient, ns)
		ginkgo.DeferCleanup(func() { gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed()) })
		clientset, err := kubernetes.NewForConfig(cfg)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		deniedSA := &corev1.ServiceAccount{Name: "no-metrics-access", Namespace: ns.Name}
		behavioral.MustCreate(ctx, k8sClient, deniedSA)
		deniedToken, err := clientset.CoreV1().ServiceAccounts(deniedSA.Namespace).CreateToken(ctx, deniedSA.Name, &authenticationv1.TokenRequest{}, metav1.CreateOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		expectResponse(endpoint, deniedToken.Status.Token, http.StatusForbidden)

		ginkgo.By("granting metrics access to a different ServiceAccount")
		sa := &corev1.ServiceAccount{Name: "metrics-reader", Namespace: ns.Name}
		behavioral.MustCreate(ctx, k8sClient, sa)
		role := &rbacv1.ClusterRole{Name: ns.Name, Rules: []rbacv1.PolicyRule{{NonResourceURLs: []string{"/metrics"}, Verbs: []string{"get"}}}}
		behavioral.MustCreate(ctx, k8sClient, role)
		ginkgo.DeferCleanup(func() { behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, role, true) })
		binding := &rbacv1.ClusterRoleBinding{
			Name:     ns.Name,
			RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role.Name},
			Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: sa.Name, Namespace: sa.Namespace}},
		}
		behavioral.MustCreate(ctx, k8sClient, binding)
		ginkgo.DeferCleanup(func() { behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, binding, true) })
		token, err := clientset.CoreV1().ServiceAccounts(sa.Namespace).CreateToken(ctx, sa.Name, &authenticationv1.TokenRequest{}, metav1.CreateOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		// Wait for RBAC propagation before the metrics filter can cache a denial.
		gomega.Eventually(func(g gomega.Gomega) {
			review, err := clientset.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
				User:                  "system:serviceaccount:" + sa.Namespace + ":" + sa.Name,
				NonResourceAttributes: &authorizationv1.NonResourceAttributes{Path: "/metrics", Verb: "get"},
			}}, metav1.CreateOptions{})
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(review.Status.Allowed).To(gomega.BeTrue())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		expectResponse(endpoint, token.Status.Token, http.StatusOK)
	})

	ginkgo.It("retains delegated authentication when explicitly selected with the feature gate enabled", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MetricsAccessControl, true)
		endpoint := startServer(new(configapi.MetricsAccessControlModeDelegated))
		expectResponse(endpoint, "", http.StatusUnauthorized)
	})
})
