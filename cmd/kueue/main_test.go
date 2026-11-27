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
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// The client-go metrics providers are process-wide, and the first one installed during package initialization wins.
// These tests run with the import graph of the kueue binary and check that the providers report to the
// controller-runtime registry, which the manager metrics endpoint serves.

func TestManagerMetricsRegistryHasRESTClientMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"major":"1","minor":"0","gitVersion":"v1.0.0"}`)
	}))
	defer srv.Close()

	client, err := discovery.NewDiscoveryClientForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatalf("Failed to create the discovery client: %v", err)
	}
	if _, err := client.ServerVersion(); err != nil {
		t.Fatalf("Failed to request the server version: %v", err)
	}

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("Failed to parse the server URL: %v", err)
	}
	for _, family := range []string{
		"rest_client_requests_total",
		"rest_client_request_duration_seconds",
		"rest_client_request_size_bytes",
		"rest_client_response_size_bytes",
	} {
		if !registryHasSeries(t, family, "host", u.Host) {
			t.Errorf("%s{host=%q} is not in the controller-runtime metrics registry", family, u.Host)
		}
	}
}

func TestManagerMetricsRegistryHasControllerWorkqueueMetrics(t *testing.T) {
	const name = "metrics_wiring_test_controller"
	c, err := controller.NewUnmanaged(name, controller.Options{
		Reconciler: reconcile.Func(func(context.Context, reconcile.Request) (reconcile.Result, error) {
			return reconcile.Result{}, nil
		}),
		SkipNameValidation: new(true),
	})
	if err != nil {
		t.Fatalf("Failed to create the controller: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		_ = c.Start(ctx)
	}()

	err = wait.PollUntilContextTimeout(ctx, 10*time.Millisecond, 10*time.Second, true, func(context.Context) (bool, error) {
		return registryHasSeries(t, "workqueue_adds_total", "name", name), nil
	})
	if err != nil {
		t.Errorf("workqueue_adds_total{name=%q} is not in the controller-runtime metrics registry: %v", name, err)
	}
}

func registryHasSeries(t *testing.T, family, labelName, labelValue string) bool {
	t.Helper()
	families, err := ctrlmetrics.Registry.Gather()
	if err != nil {
		t.Fatalf("Failed to gather the controller-runtime metrics registry: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != family {
			continue
		}
		for _, m := range mf.GetMetric() {
			if hasLabel(m, labelName, labelValue) {
				return true
			}
		}
	}
	return false
}

func hasLabel(m *dto.Metric, name, value string) bool {
	for _, l := range m.GetLabel() {
		if l.GetName() == name && l.GetValue() == value {
			return true
		}
	}
	return false
}
