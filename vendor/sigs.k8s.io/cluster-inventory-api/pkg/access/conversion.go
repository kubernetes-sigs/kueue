/*
Copyright 2026 The Kubernetes Authors.

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

package access

import (
	clientcmdv1 "k8s.io/client-go/tools/clientcmd/api/v1"

	"sigs.k8s.io/cluster-inventory-api/apis/v1alpha2"
)

// ToClientcmdCluster converts ClusterProfile API connection information into
// the kubeconfig v1 Cluster type used by client-go consumers.
//
// CertificateAuthority is left empty because ClusterProfile supports inline
// CA data only; it does not accept consumer-local CA file paths.
func ToClientcmdCluster(cluster v1alpha2.Cluster) clientcmdv1.Cluster {
	var extensions []clientcmdv1.NamedExtension
	if cluster.Extensions != nil {
		extensions = make([]clientcmdv1.NamedExtension, len(cluster.Extensions))
		for idx := range cluster.Extensions {
			extensions[idx] = clientcmdv1.NamedExtension{
				Name:      cluster.Extensions[idx].Name,
				Extension: *cluster.Extensions[idx].Extension.DeepCopy(),
			}
		}
	}

	return clientcmdv1.Cluster{
		Server:                   cluster.Server,
		TLSServerName:            cluster.TLSServerName,
		InsecureSkipTLSVerify:    cluster.InsecureSkipTLSVerify,
		CertificateAuthority:     "",
		CertificateAuthorityData: append([]byte(nil), cluster.CertificateAuthorityData...),
		ProxyURL:                 cluster.ProxyURL,
		DisableCompression:       cluster.DisableCompression,
		Extensions:               extensions,
	}
}
