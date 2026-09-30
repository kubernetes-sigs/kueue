/*
Copyright 2026 The Kubeflow Authors.

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

package statusserver

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"time"

	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	configapi "github.com/kubeflow/trainer/v2/pkg/apis/config/v1alpha1"
	"github.com/kubeflow/trainer/v2/pkg/util/cert"
)

// probeDialTimeout bounds how long a probe waits for the status server to
// complete a TLS handshake. It matches the timeout controller-runtime uses for
// the webhook server probe.
const probeDialTimeout = 10 * time.Second

func SetupServer(mgr ctrl.Manager, cfg *configapi.StatusServer, tlsOpts *configapi.TLSOptions) error {
	tlsConfig, err := cert.SetupTLSConfig(mgr, tlsOpts)
	if err != nil {
		return err
	}

	// Create a separate client with its own QPS/Burst limits
	// to avoid impacting the main reconciler's rate limits
	cli, err := createClient(mgr, cfg)
	if err != nil {
		return err
	}

	// Initialize OIDC provider for token authentication
	// The provider will be used to create verifiers with TrainJob-specific audiences
	authorizer := NewProjectedServiceAccountTokenAuthorizer(mgr.GetConfig())

	server, err := NewServer(cli, cfg, tlsConfig, authorizer)
	if err != nil {
		return err
	}
	return mgr.Add(server)
}

// RegisterProbes wires the runtime status server into the manager's healthz and
// readyz endpoints, so that Kubernetes restarts the controller if the server dies
// and training nodes only send status updates once it is able to serve them.
//
// It must be called before mgr.Start(), because controller-runtime rejects check
// registrations once the manager is running.
func RegisterProbes(mgr ctrl.Manager, cfg *configapi.StatusServer) error {
	if cfg == nil || cfg.Port == nil {
		return fmt.Errorf("status server port is required to register probes")
	}
	checker := probeChecker(fmt.Sprintf(":%d", *cfg.Port))
	if err := mgr.AddHealthzCheck("status-server-healthz", checker); err != nil {
		return fmt.Errorf("unable to set up status server health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("status-server-readyz", checker); err != nil {
		return fmt.Errorf("unable to set up status server ready check: %w", err)
	}
	return nil
}

// probeChecker returns a health checker that reports the status server as healthy
// once it accepts a TLS connection on addr.
func probeChecker(addr string) healthz.Checker {
	// The probe only verifies that the local status server is accepting TLS
	// connections, it never exchanges data with it or acts on its identity.
	// Dialing ":<port>" also gives no server name to validate the serving
	// certificate against, so certificate verification is deliberately skipped
	// here, the same way controller-runtime does it for the webhook server probe:
	// https://github.com/kubernetes-sigs/controller-runtime/blob/v0.24.1/pkg/webhook/server.go#L274-L276
	tlsCfg := &tls.Config{InsecureSkipVerify: true} //nolint:gosec // local liveness dial only, see comment above
	return func(_ *http.Request) error {
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: probeDialTimeout}, "tcp", addr, tlsCfg)
		if err != nil {
			return fmt.Errorf("status server is not reachable at %s: %w", addr, err)
		}
		if err := conn.Close(); err != nil {
			return fmt.Errorf("status server is not reachable at %s: closing connection: %w", addr, err)
		}
		return nil
	}
}

func createClient(mgr ctrl.Manager, cfg *configapi.StatusServer) (client.Client, error) {
	// Copy the manager's rest config and override rate limits
	mgrCfg := rest.CopyConfig(mgr.GetConfig())
	if cfg.QPS != nil {
		mgrCfg.QPS = *cfg.QPS
	}
	if cfg.Burst != nil {
		mgrCfg.Burst = int(*cfg.Burst)
	}

	cli, err := client.New(mgrCfg, client.Options{
		Scheme: mgr.GetScheme(),
		Mapper: mgr.GetRESTMapper(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create status server client: %w", err)
	}

	return cli, nil
}
