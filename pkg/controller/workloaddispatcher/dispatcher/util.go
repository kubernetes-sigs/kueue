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

package dispatcher

import (
	"context"
	"errors"

	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/util/multikueuehelper"
	"sigs.k8s.io/kueue/pkg/workload"
)

func GetMultiKueueAdmissionCheck(ctx context.Context, c client.Client, local *kueue.Workload) (*kueue.AdmissionCheckState, error) {
	relevantChecks, err := admissioncheck.FilterForController(ctx, c, local.Status.AdmissionChecks, kueue.MultiKueueControllerName)
	if err != nil {
		return nil, err
	}

	if len(relevantChecks) == 0 {
		return nil, nil
	}
	return workload.FindAdmissionCheck(local.Status.AdmissionChecks, relevantChecks[0]), nil
}

func GetRemoteClusters(ctx context.Context, cl client.Client, helper *multikueuehelper.MultiKueueStoreHelper, localWl *kueue.Workload, acName kueue.AdmissionCheckReference) (sets.Set[string], error) {
	cfg, err := helper.ConfigForAdmissionCheck(ctx, acName)
	if err != nil {
		return nil, err
	}
	if len(cfg.Spec.Clusters) == 0 {
		return nil, errors.New("no active clusters")
	}

	remoteClusters := sets.New[string]()
	for _, clusterName := range cfg.Spec.Clusters {
		remoteClusters.Insert(clusterName)
	}

	return remoteClusters, nil
}
