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

package api

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	genericapiserver "k8s.io/apiserver/pkg/server"

	visibility "sigs.k8s.io/kueue/apis/visibility/v1beta1"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	visibilityapi "sigs.k8s.io/kueue/pkg/visibility/api/v1beta1"
)

var (
	Scheme         = runtime.NewScheme()
	Codecs         = serializer.NewCodecFactory(Scheme)
	ParameterCodec = runtime.NewParameterCodec(Scheme)
)

func init() {
	utilruntime.Must(visibility.AddToScheme(Scheme))
	utilruntime.Must(Scheme.SetVersionPriority(visibility.GroupVersion))
	metav1.AddToGroupVersion(Scheme, schema.GroupVersion{Version: "v1"})
}

// Install installs API scheme and registers storages
func Install(server *genericapiserver.GenericAPIServer, kueueMgr *qcache.Manager) error {
	apiGroupInfo := genericapiserver.NewDefaultAPIGroupInfo(visibility.GroupVersion.Group, Scheme, ParameterCodec, Codecs)
	apiGroupInfo.VersionedResourcesStorageMap[visibility.GroupVersion.Version] = visibilityapi.NewStorage(kueueMgr)
	return server.InstallAPIGroups(&apiGroupInfo)
}
