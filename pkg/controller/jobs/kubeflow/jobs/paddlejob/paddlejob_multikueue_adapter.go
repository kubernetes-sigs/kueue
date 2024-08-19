/*
Copyright 2024 The Kubernetes Authors.

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

package paddlejob

import (
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/kubeflowjob"
	"sigs.k8s.io/kueue/pkg/util/api"
)

var _ jobframework.MultiKueueAdapter = kubeflowjob.NewMKAdapter(copyJobSpec, copyJobStatus, getEmptyList, gvk)

func copyJobStatus(dst, src *kftraining.PaddleJob) {
	dst.Status = src.Status
}

func copyJobSpec(dst, src *kftraining.PaddleJob) {
	*dst = kftraining.PaddleJob{
		ObjectMeta: api.CloneObjectMetaForCreation(&src.ObjectMeta),
		Spec:       *src.Spec.DeepCopy(),
	}
}

func getEmptyList() client.ObjectList {
	return &kftraining.PaddleJobList{}
}
