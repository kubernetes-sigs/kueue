package credentials

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"

	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	clientcmdlatest "k8s.io/client-go/tools/clientcmd/api/latest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
)

// client.authentication.k8s.io/exec is a reserved extension key defined by the Kubernetes
// client authentication API (SIG Auth), not by the ClusterProfile API.
// Reference:
// https://kubernetes.io/docs/reference/config-api/client-authentication.v1beta1/#client-authentication-k8s-io-v1beta1-Cluster
const clusterExtensionKey = "client.authentication.k8s.io/exec"

type Provider struct {
	Name       string                   `json:"name"`
	ExecConfig *clientcmdapi.ExecConfig `json:"execConfig"`
}

type CredentialsProvider struct {
	Providers []Provider `json:"providers"`
}

func New(providers []Provider) *CredentialsProvider {
	return &CredentialsProvider{
		Providers: providers,
	}
}

// SetupProviderFileFlag defines the -clusterprofile-provider-file command-line flag and returns a pointer
// to the string that will hold the path. flag.Parse() must still be called manually by the caller
func SetupProviderFileFlag() *string {
	return flag.String("clusterprofile-provider-file", "clusterprofile-provider-file.json", "Path to the JSON configuration file")
}

func NewFromFile(path string) (*CredentialsProvider, error) {
	// 1. Read the file's content
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read credentials file: %w", err)
	}

	// 2. Create a new Providers instance and unmarshal the data into it
	var providers CredentialsProvider
	if err := json.Unmarshal(data, &providers); err != nil {
		return nil, fmt.Errorf("failed to unmarshal credential proviers: %w", err)
	}

	// 3. Return the populated credentials
	return &providers, nil
}

// BuildConfigFromCP builds a rest.Config from the given ClusterProfile
func (cp *CredentialsProvider) BuildConfigFromCP(clusterprofile *v1alpha1.ClusterProfile) (*rest.Config, error) {
	// 1. obtain the correct clusterAccessor from the CP
	clusterAccessor := cp.getClusterAccessorFromClusterProfile(clusterprofile)
	if clusterAccessor == nil {
		return nil, fmt.Errorf("no matching cluster accessor found for cluster profile %q", clusterprofile.Name)
	}

	// 2. Get Exec Config
	execConfig := cp.getExecConfigFromConfig(clusterAccessor.Name)
	if execConfig == nil {
		return nil, fmt.Errorf("no exec credentials found for provider %q", clusterAccessor.Name)
	}

	// 3. build resulting rest.Config
	config := &rest.Config{
		Host: clusterAccessor.Cluster.Server,
		TLSClientConfig: rest.TLSClientConfig{
			CAData: clusterAccessor.Cluster.CertificateAuthorityData,
		},
		Proxy: func(request *http.Request) (*url.URL, error) {
			if clusterAccessor.Cluster.ProxyURL == "" {
				return nil, nil
			}
			return url.Parse(clusterAccessor.Cluster.ProxyURL)
		},
	}

	config.ExecProvider = &clientcmdapi.ExecConfig{
		APIVersion:         execConfig.APIVersion,
		Command:            execConfig.Command,
		Args:               execConfig.Args,
		Env:                execConfig.Env,
		InteractiveMode:    "Never",
		ProvideClusterInfo: execConfig.ProvideClusterInfo,
	}

	// Propagate reserved extension into ExecCredential.Spec.Cluster.Config if present
	internalCluster := clientcmdapi.NewCluster()
	if err := clientcmdlatest.Scheme.Convert(&clusterAccessor.Cluster, internalCluster, nil); err != nil {
		return nil, fmt.Errorf("failed to convert v1 Cluster to internal: %w", err)
	}
	config.ExecProvider.Config = internalCluster.Extensions[clusterExtensionKey]

	return config, nil
}

func (cp *CredentialsProvider) getExecConfigFromConfig(providerName string) *clientcmdapi.ExecConfig {
	for _, provider := range cp.Providers {
		if provider.Name == providerName {
			return provider.ExecConfig
		}
	}
	return nil
}

// getClusterAccessorFromClusterProfile returns the first AccessProvider from the ClusterProfile
// that matches one of the supported provider types in the CredentialsProvider
func (cp *CredentialsProvider) getClusterAccessorFromClusterProfile(cluster *v1alpha1.ClusterProfile) *v1alpha1.AccessProvider {
	accessProviderTypes := map[string]*v1alpha1.AccessProvider{}

	// to keep backward compatibility, we first check the CredentialProviders field
	for _, accessProvider := range cluster.Status.CredentialProviders {
		accessProviderTypes[accessProvider.Name] = accessProvider.DeepCopy()
		klog.Warningf("ClusterProfile %q uses deprecated field CredentialProviders %q; please migrate to AccessProviders", cluster.Name, accessProvider.Name)
	}

	for _, accessProvider := range cluster.Status.AccessProviders {
		accessProviderTypes[accessProvider.Name] = accessProvider.DeepCopy()
	}

	// we return the first access provider that the CredentialsProvider supports.
	for _, providerType := range cp.Providers {
		if accessor, found := accessProviderTypes[providerType.Name]; found {
			return accessor
		}
	}
	return nil
}
