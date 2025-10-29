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

package testing

import (
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

type TestKubeconfigWrapper struct {
	Config clientcmdapi.Config
}

func NewTestKubeConfigWrapper() *TestKubeconfigWrapper {
	return &TestKubeconfigWrapper{
		Config: clientcmdapi.Config{
			Kind:       "config",
			APIVersion: "v1",
			Clusters:   map[string]*clientcmdapi.Cluster{},
			AuthInfos:  map[string]*clientcmdapi.AuthInfo{},
			Contexts:   map[string]*clientcmdapi.Context{},
		},
	}
}

func (k *TestKubeconfigWrapper) Cluster(name, server string, caData []byte) *TestKubeconfigWrapper {
	k.Config.Clusters[name] = &clientcmdapi.Cluster{
		Server:                   server,
		CertificateAuthorityData: caData,
	}
	return k
}

func (k *TestKubeconfigWrapper) User(name string, certData, keyData []byte) *TestKubeconfigWrapper {
	k.Config.AuthInfos[name] = &clientcmdapi.AuthInfo{
		ClientCertificateData: certData,
		ClientKeyData:         keyData,
	}
	return k
}

func (k *TestKubeconfigWrapper) Context(name, clusterName, userName string) *TestKubeconfigWrapper {
	k.Config.Contexts[name] = &clientcmdapi.Context{
		Cluster:  clusterName,
		AuthInfo: userName,
	}
	return k
}

func (k *TestKubeconfigWrapper) CurrentContext(name string) *TestKubeconfigWrapper {
	k.Config.CurrentContext = name
	return k
}

func (k *TestKubeconfigWrapper) TokenAuthInfo(name, token string) *TestKubeconfigWrapper {
	k.Config.AuthInfos[name].Token = token
	return k
}

func (k *TestKubeconfigWrapper) TokenFileAuthInfo(name, tokenFilePath string) *TestKubeconfigWrapper {
	k.Config.AuthInfos[name].TokenFile = tokenFilePath
	return k
}

func (k *TestKubeconfigWrapper) InsecureSkipTLSVerify(clusterName string, skip bool) *TestKubeconfigWrapper {
	k.Config.Clusters[clusterName].InsecureSkipTLSVerify = skip
	return k
}

func (k *TestKubeconfigWrapper) CAFileCluster(clusterName, caFilePath string) *TestKubeconfigWrapper {
	k.Config.Clusters[clusterName].CertificateAuthority = caFilePath
	return k
}

func (k *TestKubeconfigWrapper) Clone() *TestKubeconfigWrapper {
	return &TestKubeconfigWrapper{Config: *k.Config.DeepCopy()}
}

func (k *TestKubeconfigWrapper) Obj() clientcmdapi.Config {
	return k.Config
}

func (k *TestKubeconfigWrapper) Build() ([]byte, error) {
	return clientcmd.Write(k.Config)
}

func RestConfigToKubeConfig(restConfig *rest.Config) ([]byte, error) {
	return NewTestKubeConfigWrapper().Cluster("default-cluster", restConfig.Host, restConfig.CAData).
		User("default-user", restConfig.CertData, restConfig.KeyData).
		Context("default-context", "default-cluster", "default-user").
		CurrentContext("default-context").Build()
}
