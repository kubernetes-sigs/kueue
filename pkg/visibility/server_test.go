package visibility

import (
	"flag"
	"testing"
)

func TestCreateVisibilityServerOptions_Kubeconfig(t *testing.T) {
	// Ensure the flag is registered so we don't panic or fail the lookup.
	originalFlag := flag.Lookup("kubeconfig")
	var originalValue string
	if originalFlag == nil {
		flag.String("kubeconfig", "", "path to kubeconfig")
	} else {
		originalValue = originalFlag.Value.String()
		// Defer restoring the original value to avoid polluting other tests
		defer func() {
			_ = flag.Set("kubeconfig", originalValue)
		}()
	}

	testCases := []struct {
		name           string
		kubeConfigPath string
		expectedPath   string
	}{
		{
			name:           "flag is empty",
			kubeConfigPath: "",
			expectedPath:   "",
		},
		{
			name:           "flag is set to a valid path",
			kubeConfigPath: "/fake/path/to/admin.conf",
			expectedPath:   "/fake/path/to/admin.conf",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Set the global flag to match the test case
			if err := flag.Set("kubeconfig", tc.kubeConfigPath); err != nil {
				t.Fatalf("failed to set kubeconfig flag: %v", err)
			}

			// Call the function
			options, err := createVisibilityServerOptions(false)
			if err != nil {
				t.Fatalf("unexpected error from createVisibilityServerOptions: %v", err)
			}

			// Validate the outputs
			if options.Authentication.RemoteKubeConfigFile != tc.expectedPath {
				t.Errorf("expected Authentication.RemoteKubeConfigFile to be %q, got %q",
					tc.expectedPath, options.Authentication.RemoteKubeConfigFile)
			}

			if options.Authorization.RemoteKubeConfigFile != tc.expectedPath {
				t.Errorf("expected Authorization.RemoteKubeConfigFile to be %q, got %q",
					tc.expectedPath, options.Authorization.RemoteKubeConfigFile)
			}
		})
	}
}
