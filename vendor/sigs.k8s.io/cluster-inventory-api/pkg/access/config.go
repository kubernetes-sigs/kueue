// Package access provides configuration for building Kubernetes REST configs
// from ClusterProfile resources.
//
// This package is used by controllers that need to connect to "spoke" clusters
// registered in a cluster inventory. It reads a provider configuration file
// (typically passed via the --clusterprofile-provider-file flag), matches
// providers against the AccessProviders listed in a ClusterProfile's status,
// and produces a [rest.Config] ready for use with client-go.
//
// Note: This package is unrelated to Kubernetes RBAC or access control.
// It manages cluster access configuration via exec-based authentication plugins.
//
// Basic usage:
//
//	// Load provider configuration from a JSON file
//	cfg, err := access.NewFromFile("clusterprofile-provider-file.json")
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	// Build a rest.Config for a specific ClusterProfile
//	restConfig, err := cfg.BuildConfigFromCP(clusterProfile)
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	// Use restConfig with client-go
//	client, err := kubernetes.NewForConfig(restConfig)
package access

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"

	"gopkg.in/yaml.v3"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	clientcmdlatest "k8s.io/client-go/tools/clientcmd/api/latest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
)

const (
	// client.authentication.k8s.io/exec is a reserved extension key defined
	// by the Kubernetes client authentication API (SIG Auth), not by the
	// ClusterProfile API.
	// Reference: https://kubernetes.io/docs/reference/config-api/
	//   client-authentication.v1beta1/
	//   #client-authentication-k8s-io-v1beta1-Cluster
	clusterExecExtensionKey = "client.authentication.k8s.io/exec"

	// additionalCLIArgsExtensionKey and additionalEnvVarsExtensionKey are
	// two reserved extensions defined in KEP 5339, which allows users to pass in (usually cluster-specific)
	// additional command-line arguments and environment variables to the exec plugin from
	// the ClusterProfile API side.
	additionalCLIArgsExtensionKey = "clusterprofiles.multicluster.x-k8s.io/exec/additional-args"
	additionalEnvVarsExtensionKey = "clusterprofiles.multicluster.x-k8s.io/exec/additional-envs"
)

type ProfileSourcedCLIArgsPolicy string

const (
	ProfileSourcedCLIArgsPolicyAppend ProfileSourcedCLIArgsPolicy = "Append"
	ProfileSourcedCLIArgsPolicyIgnore ProfileSourcedCLIArgsPolicy = "Ignore"
)

type ProfileSourcedEnvVarsPolicy string

const (
	ProfileSourcedEnvVarsPolicyAppendIfNotExists ProfileSourcedEnvVarsPolicy = "AppendIfNotExists"
	ProfileSourcedEnvVarsPolicyReplace           ProfileSourcedEnvVarsPolicy = "Replace"
	ProfileSourcedEnvVarsPolicyIgnore            ProfileSourcedEnvVarsPolicy = "Ignore"
)

type Provider struct {
	Name                        string                      `json:"name"`
	ExecConfig                  *clientcmdapi.ExecConfig    `json:"execConfig"`
	ProfileSourcedCLIArgsPolicy ProfileSourcedCLIArgsPolicy `json:"profileSourcedCLIArgsPolicy,omitempty"`
	ProfileSourcedEnvVarsPolicy ProfileSourcedEnvVarsPolicy `json:"profileSourcedEnvVarsPolicy,omitempty"`
}

type Config struct {
	Providers []Provider `json:"providers"`
}

func New(providers []Provider) *Config {
	return &Config{
		Providers: providers,
	}
}

// SetupProviderFileFlag defines the -clusterprofile-provider-file command-line flag
// and returns a pointer to the string that will hold the path.
// flag.Parse() must still be called manually by the caller
func SetupProviderFileFlag() *string {
	return flag.String(
		"clusterprofile-provider-file",
		"clusterprofile-provider-file.json",
		"Path to the JSON configuration file",
	)
}

func NewFromFile(path string) (*Config, error) {
	// 1. Read the file's content
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read access config file: %w", err)
	}

	// 2. Create a new Providers instance and unmarshal the data into it
	var providers Config
	if err := json.Unmarshal(data, &providers); err != nil {
		return nil, fmt.Errorf("failed to unmarshal access providers: %w", err)
	}

	// 3. Return the populated access config
	return &providers, nil
}

// BuildConfigFromCP builds a rest.Config from the given ClusterProfile
func (c *Config) BuildConfigFromCP(clusterprofile *v1alpha1.ClusterProfile) (*rest.Config, error) {
	// 1. obtain the correct clusterAccessor from the CP
	clusterAccessor := c.getClusterAccessorFromClusterProfile(clusterprofile)
	if clusterAccessor == nil {
		return nil, fmt.Errorf("no matching cluster accessor found for cluster profile %q", clusterprofile.Name)
	}

	// 2. Get Exec Config
	execConfig, cliArgsPolicy, envVarsPolicy :=
		c.getExecConfigAndFlagsFromConfig(clusterAccessor.Name)
	if execConfig == nil {
		return nil, fmt.Errorf(
			"no exec config found for provider %q",
			clusterAccessor.Name,
		)
	}

	// 3. Add additional CLI arguments and environment variables
	// from cluster extensions if allowed.
	for idx := range clusterAccessor.Cluster.Extensions {
		ext := &clusterAccessor.Cluster.Extensions[idx]

		switch ext.Name {
		case additionalCLIArgsExtensionKey:
			if err := processClusterProfileSourcedCLIArgData(
				execConfig, ext.Extension.Raw, cliArgsPolicy,
			); err != nil {
				return nil, err
			}
		case additionalEnvVarsExtensionKey:
			if err := processClusterProfileSourcedEnvVarData(
				execConfig, ext.Extension.Raw, envVarsPolicy,
			); err != nil {
				return nil, err
			}
		}
	}

	// 4. build resulting rest.Config
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
		Config:             execConfig.Config,
	}

	// Propagate reserved extension into ExecCredential.Spec.Cluster.Config if present
	internalCluster := clientcmdapi.NewCluster()
	if err := clientcmdlatest.Scheme.Convert(&clusterAccessor.Cluster, internalCluster, nil); err != nil {
		return nil, fmt.Errorf("failed to convert v1 Cluster to internal: %w", err)
	}
	if extData, ok := internalCluster.Extensions[clusterExecExtensionKey]; ok {
		config.ExecProvider.Config = extData
	}

	return config, nil
}

func (c *Config) getExecConfigAndFlagsFromConfig(
	providerName string,
) (*clientcmdapi.ExecConfig, ProfileSourcedCLIArgsPolicy, ProfileSourcedEnvVarsPolicy) {
	for _, provider := range c.Providers {
		if provider.Name == providerName {
			return provider.ExecConfig, provider.ProfileSourcedCLIArgsPolicy, provider.ProfileSourcedEnvVarsPolicy
		}
	}
	return nil, ProfileSourcedCLIArgsPolicyIgnore, ProfileSourcedEnvVarsPolicyIgnore
}

// getClusterAccessorFromClusterProfile returns the first AccessProvider from the ClusterProfile
// that matches one of the supported provider types in the Config
func (c *Config) getClusterAccessorFromClusterProfile(
	cluster *v1alpha1.ClusterProfile,
) *v1alpha1.AccessProvider {
	accessProviderTypes := map[string]*v1alpha1.AccessProvider{}

	// to keep backward compatibility, we first check the CredentialProviders field
	for _, accessProvider := range cluster.Status.CredentialProviders {
		accessProviderTypes[accessProvider.Name] = accessProvider.DeepCopy()
		klog.Warningf(
			"ClusterProfile %q uses deprecated field CredentialProviders %q; please migrate to AccessProviders",
			cluster.Name, accessProvider.Name,
		)
	}

	for _, accessProvider := range cluster.Status.AccessProviders {
		accessProviderTypes[accessProvider.Name] = accessProvider.DeepCopy()
	}

	// we return the first access provider that the Config supports.
	for _, providerType := range c.Providers {
		if accessor, found := accessProviderTypes[providerType.Name]; found {
			return accessor
		}
	}
	return nil
}

func processClusterProfileSourcedCLIArgData(
	execConfig *clientcmdapi.ExecConfig,
	data []byte,
	policy ProfileSourcedCLIArgsPolicy,
) error {
	switch policy {
	case "", ProfileSourcedCLIArgsPolicyIgnore:
		// No action is needed.
		return nil
	case ProfileSourcedCLIArgsPolicyAppend:
		var additionalArgs []string
		if err := yaml.Unmarshal(data, &additionalArgs); err != nil {
			return fmt.Errorf("failed to unmarshal additional CLI args extension: %w", err)
		}
		execConfig.Args = append(execConfig.Args, additionalArgs...)
		return nil
	default:
		// The policy is not supported.
		return fmt.Errorf("unsupported ProfileSourcedCLIArgsPolicy: %q", policy)
	}
}

func processClusterProfileSourcedEnvVarData(
	execConfig *clientcmdapi.ExecConfig,
	data []byte,
	policy ProfileSourcedEnvVarsPolicy,
) error {
	var envVars map[string]string

	switch policy {
	case "", ProfileSourcedEnvVarsPolicyIgnore:
		// No action is needed.
		return nil
	case ProfileSourcedEnvVarsPolicyAppendIfNotExists:
		if err := yaml.Unmarshal(data, &envVars); err != nil {
			return fmt.Errorf("failed to unmarshal additional env vars extension: %w", err)
		}

		// Add existing environment variables. If the same variable is specified twice
		// in both the extension data and the execConfig data, the value in the execConfig data takes precedence.
		for idx := range execConfig.Env {
			env := &execConfig.Env[idx]
			envVars[env.Name] = env.Value
		}
	case ProfileSourcedEnvVarsPolicyReplace:
		if err := yaml.Unmarshal(data, &envVars); err != nil {
			return fmt.Errorf("failed to unmarshal additional env vars extension: %w", err)
		}

		// Add existing environment variables. If the same variable is specified twice
		// in both the extension data and the execConfig data, the value in the extension data takes precedence.
		for idx := range execConfig.Env {
			env := &execConfig.Env[idx]
			if _, exists := envVars[env.Name]; !exists {
				envVars[env.Name] = env.Value
			}
		}
	default:
		// The policy is not supported.
		return fmt.Errorf("unsupported ProfileSourcedEnvVarsPolicy: %q", policy)
	}

	// Write the processed list back to the execConfig in the expected format.
	envVarList := make([]clientcmdapi.ExecEnvVar, 0, len(envVars))
	for name, value := range envVars {
		envVarList = append(envVarList, clientcmdapi.ExecEnvVar{
			Name:  name,
			Value: value,
		})
	}
	execConfig.Env = envVarList
	return nil
}
