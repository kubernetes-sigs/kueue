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

package jobs

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/manager"

	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/test/integration/framework"
)

type SetupFunc func(opts ...jobframework.Option) framework.ManagerSetup

func ManagerSetupWithCache(managerSetup SetupFunc, opts ...jobframework.Option) framework.ManagerSetup {
	return func(ctx context.Context, mgr manager.Manager) {
		cCache := schdcache.New(mgr.GetClient())
		opts = append(opts, jobframework.WithCache(cCache))

		managerSetup(opts...)(ctx, mgr)
	}
}
