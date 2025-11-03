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

package jobframework

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlmgr "sigs.k8s.io/controller-runtime/pkg/manager"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestWaitForAPI_RecoverAfterCRDReinstall(t *testing.T) {
	t.Helper()

	gvk := schema.GroupVersionKind{Group: "jobset.x-k8s.io", Version: "v1alpha2", Kind: "JobSet"}
	crd := &apiextensionsv1.CustomResourceDefinition{
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: gvk.Group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{Kind: gvk.Kind},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name:    gvk.Version,
				Storage: true,
			}},
		},
	}

	ctx, logger := utiltesting.ContextWithLog(t)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	k8sClient := utiltesting.NewClientBuilder(jobset.AddToScheme).Build()
	mgrOpts := ctrlmgr.Options{
		Scheme: k8sClient.Scheme(),
		NewClient: func(*rest.Config, client.Options) (client.Client, error) {
			return k8sClient, nil
		},
		MapperProvider: func(*rest.Config, *http.Client) (apimeta.RESTMapper, error) {
			mapper := apimeta.NewDefaultRESTMapper([]schema.GroupVersion{gvk.GroupVersion()})
			testMapper := &TestRESTMapper{DefaultRESTMapper: mapper, lock: sync.RWMutex{}}
			return testMapper, nil
		},
	}
	mgr, err := ctrlmgr.New(&rest.Config{}, mgrOpts)
	if err != nil {
		t.Fatalf("failed to create manager: %v", err)
	}

	manager := integrationManager{
		crdNotifiers: map[schema.GroupVersionKind]chan struct{}{
			gvk: make(chan struct{}),
		},
	}

	done := make(chan struct{})
	go func() {
		manager.waitForAPI(ctx, mgr, logger, gvk, func() {
			close(done)
		})
	}()

	// Simulate CRD deletion while the waiter is still blocked on the original channel.
	manager.handleCRDDeletion(logger, crd)

	select {
	case <-done:
		t.Fatal("waitForAPI finished before the CRD was reinstalled")
	case <-time.After(50 * time.Millisecond):
	}

	mapper := mgr.GetRESTMapper().(*TestRESTMapper)
	mapper.lock.Lock()
	mapper.Add(gvk, apimeta.RESTScopeNamespace)
	mapper.lock.Unlock()

	manager.notifyCRDAvailable(logger, crd)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("waitForAPI did not recover after CRD reinstall")
	}
}