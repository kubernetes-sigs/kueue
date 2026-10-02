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

package util

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// rbacResources holds parsed RBAC Kubernetes manifests.
type rbacResources struct {
	serviceAccounts     map[string]*corev1.ServiceAccount
	roles               map[string]*rbacv1.Role
	clusterRoles        map[string]*rbacv1.ClusterRole
	roleBindings        map[string]*rbacv1.RoleBinding
	clusterRoleBindings map[string]*rbacv1.ClusterRoleBinding
}

// newRBACResources initializes an empty rbacResources container.
func newRBACResources() *rbacResources {
	return &rbacResources{
		serviceAccounts:     make(map[string]*corev1.ServiceAccount),
		roles:               make(map[string]*rbacv1.Role),
		clusterRoles:        make(map[string]*rbacv1.ClusterRole),
		roleBindings:        make(map[string]*rbacv1.RoleBinding),
		clusterRoleBindings: make(map[string]*rbacv1.ClusterRoleBinding),
	}
}

// kustomization holds the resources list declared in a kustomization.yaml file.
type kustomization struct {
	Resources []string `json:"resources"`
}

// loadRBACResourcesFromKustomization parses kustomization.yaml in dir and loads all referenced manifests.
func loadRBACResourcesFromKustomization(dir string) (*rbacResources, error) {
	kustPath := filepath.Join(dir, "kustomization.yaml")
	kustBytes, err := os.ReadFile(kustPath)
	if err != nil {
		return nil, fmt.Errorf("reading kustomization %s: %w", kustPath, err)
	}

	var kust kustomization
	if err := yaml.Unmarshal(kustBytes, &kust); err != nil {
		return nil, fmt.Errorf("parsing kustomization %s: %w", kustPath, err)
	}

	res := newRBACResources()
	for _, resName := range kust.Resources {
		if !strings.HasSuffix(resName, ".yaml") && !strings.HasSuffix(resName, ".yml") {
			continue
		}

		path := filepath.Join(dir, resName)
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading resource %s: %w", path, err)
		}

		decoder := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(content), 4096)
		for {
			var raw map[string]any
			if err := decoder.Decode(&raw); err != nil {
				if err == io.EOF {
					break
				}
				return nil, fmt.Errorf("decoding yaml doc in %s: %w", path, err)
			}
			if len(raw) == 0 {
				continue
			}

			kindVal, _ := raw["kind"].(string)
			switch kindVal {
			case "ServiceAccount":
				var sa corev1.ServiceAccount
				docBytes, _ := yaml.Marshal(raw)
				if err := yaml.Unmarshal(docBytes, &sa); err != nil {
					return nil, fmt.Errorf("unmarshaling ServiceAccount in %s: %w", path, err)
				}
				key := sa.Name
				if sa.Namespace != "" {
					key = sa.Namespace + "/" + sa.Name
				}
				res.serviceAccounts[key] = &sa
			case "Role":
				var r rbacv1.Role
				docBytes, _ := yaml.Marshal(raw)
				if err := yaml.Unmarshal(docBytes, &r); err != nil {
					return nil, fmt.Errorf("unmarshaling Role in %s: %w", path, err)
				}
				key := r.Name
				if r.Namespace != "" {
					key = r.Namespace + "/" + r.Name
				}
				res.roles[key] = &r
			case "ClusterRole":
				var cr rbacv1.ClusterRole
				docBytes, _ := yaml.Marshal(raw)
				if err := yaml.Unmarshal(docBytes, &cr); err != nil {
					return nil, fmt.Errorf("unmarshaling ClusterRole in %s: %w", path, err)
				}
				res.clusterRoles[cr.Name] = &cr
			case "RoleBinding":
				var rb rbacv1.RoleBinding
				docBytes, _ := yaml.Marshal(raw)
				if err := yaml.Unmarshal(docBytes, &rb); err != nil {
					return nil, fmt.Errorf("unmarshaling RoleBinding in %s: %w", path, err)
				}
				key := rb.Name
				if rb.Namespace != "" {
					key = rb.Namespace + "/" + rb.Name
				}
				res.roleBindings[key] = &rb
			case "ClusterRoleBinding":
				var crb rbacv1.ClusterRoleBinding
				docBytes, _ := yaml.Marshal(raw)
				if err := yaml.Unmarshal(docBytes, &crb); err != nil {
					return nil, fmt.Errorf("unmarshaling ClusterRoleBinding in %s: %w", path, err)
				}
				res.clusterRoleBindings[crb.Name] = &crb
			}
		}
	}

	return res, nil
}

// validateRBACRelationships checks that all bindings target the expected ServiceAccount and valid roles.
func validateRBACRelationships(res *rbacResources, targetSAName, targetSANamespace string) []error {
	var errs []error

	saKey := targetSAName
	if targetSANamespace != "" {
		saKey = targetSANamespace + "/" + targetSAName
	}
	if _, ok := res.serviceAccounts[saKey]; !ok {
		errs = append(errs, fmt.Errorf("controller ServiceAccount %q not found", saKey))
	}

	// Validate RoleBindings
	for name, rb := range res.roleBindings {
		if len(rb.Subjects) == 0 {
			errs = append(errs, fmt.Errorf("RoleBinding %q has no subjects", name))
		} else {
			for _, subj := range rb.Subjects {
				if subj.Kind != "ServiceAccount" || subj.Name != targetSAName || subj.Namespace != targetSANamespace || subj.APIGroup != "" {
					errs = append(errs, fmt.Errorf("RoleBinding %q has unexpected subject: kind=%s, name=%s, namespace=%s, apiGroup=%s", name, subj.Kind, subj.Name, subj.Namespace, subj.APIGroup))
				}
			}
		}

		if rb.RoleRef.APIGroup != rbacv1.GroupName {
			errs = append(errs, fmt.Errorf("RoleBinding %q roleRef apiGroup must be %q, got %q", name, rbacv1.GroupName, rb.RoleRef.APIGroup))
		}

		switch rb.RoleRef.Kind {
		case "Role":
			roleKey := rb.RoleRef.Name
			if rb.Namespace != "" {
				roleKey = rb.Namespace + "/" + rb.RoleRef.Name
			}
			if _, ok := res.roles[roleKey]; !ok {
				errs = append(errs, fmt.Errorf("RoleBinding %q references non-existent Role %q", name, roleKey))
			}
		case "ClusterRole":
			if _, ok := res.clusterRoles[rb.RoleRef.Name]; !ok {
				errs = append(errs, fmt.Errorf("RoleBinding %q references non-existent ClusterRole %q", name, rb.RoleRef.Name))
			}
		default:
			errs = append(errs, fmt.Errorf("RoleBinding %q has invalid roleRef kind %q", name, rb.RoleRef.Kind))
		}
	}

	// Validate ClusterRoleBindings
	for name, crb := range res.clusterRoleBindings {
		if len(crb.Subjects) == 0 {
			errs = append(errs, fmt.Errorf("ClusterRoleBinding %q has no subjects", name))
		} else {
			for _, subj := range crb.Subjects {
				if subj.Kind != "ServiceAccount" || subj.Name != targetSAName || subj.Namespace != targetSANamespace || subj.APIGroup != "" {
					errs = append(
						errs,
						fmt.Errorf("ClusterRoleBinding %q has unexpected subject: kind=%s, name=%s, namespace=%s, apiGroup=%s", name, subj.Kind, subj.Name, subj.Namespace, subj.APIGroup),
					)
				}
			}
		}

		if crb.RoleRef.APIGroup != rbacv1.GroupName {
			errs = append(errs, fmt.Errorf("ClusterRoleBinding %q roleRef apiGroup must be %q, got %q", name, rbacv1.GroupName, crb.RoleRef.APIGroup))
		}

		if crb.RoleRef.Kind != "ClusterRole" {
			errs = append(errs, fmt.Errorf("ClusterRoleBinding %q roleRef kind must be ClusterRole, got %q", name, crb.RoleRef.Kind))
		} else if _, ok := res.clusterRoles[crb.RoleRef.Name]; !ok {
			errs = append(errs, fmt.Errorf("ClusterRoleBinding %q references non-existent ClusterRole %q", name, crb.RoleRef.Name))
		}
	}

	return errs
}

// findRBACDir locates the config/components/rbac directory relative to current working directory.
func findRBACDir() (string, error) {
	candidates := []string{
		"config/components/rbac",
		"../../config/components/rbac",
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			return c, nil
		}
	}
	return "", errors.New("could not find config/components/rbac directory")
}

// TestRBACManifests verifies the generated RBAC bindings against controller ServiceAccount and roles.
func TestRBACManifests(t *testing.T) {
	t.Run("VerifyGeneratedRBACManifests", func(t *testing.T) {
		rbacDir, err := findRBACDir()
		if err != nil {
			t.Fatalf("Failed to locate RBAC directory: %v", err)
		}

		res, err := loadRBACResourcesFromKustomization(rbacDir)
		if err != nil {
			t.Fatalf("Failed to load RBAC resources from kustomization: %v", err)
		}

		expectedRoleBindings := []string{
			"system/leader-election-rolebinding",
			"system/manager-clusterprofiles-rolebinding",
			"system/manager-secrets-rolebinding",
		}
		for _, name := range expectedRoleBindings {
			if _, ok := res.roleBindings[name]; !ok {
				t.Errorf("Expected RoleBinding %q not found in kustomization resources", name)
			}
		}

		expectedClusterRoleBindings := []string{
			"manager-rolebinding",
			"metrics-auth-rolebinding",
		}
		for _, name := range expectedClusterRoleBindings {
			if _, ok := res.clusterRoleBindings[name]; !ok {
				t.Errorf("Expected ClusterRoleBinding %q not found in kustomization resources", name)
			}
		}

		totalBindings := len(res.roleBindings) + len(res.clusterRoleBindings)
		expectedCount := len(expectedRoleBindings) + len(expectedClusterRoleBindings)
		if totalBindings != expectedCount {
			t.Fatalf("Expected exactly %d bindings in %s, found %d (RoleBindings: %d, ClusterRoleBindings: %d)",
				expectedCount, rbacDir, totalBindings, len(res.roleBindings), len(res.clusterRoleBindings))
		}

		expectedRoleRefs := map[string]rbacv1.RoleRef{
			"system/leader-election-rolebinding": {
				Kind: "Role",
				Name: "leader-election-role",
			},
			"system/manager-clusterprofiles-rolebinding": {
				Kind: "Role",
				Name: "manager-clusterprofiles-role",
			},
			"system/manager-secrets-rolebinding": {
				Kind: "Role",
				Name: "manager-secrets-role",
			},
			"manager-rolebinding": {
				Kind: "ClusterRole",
				Name: "manager-role",
			},
			"metrics-auth-rolebinding": {
				Kind: "ClusterRole",
				Name: "metrics-auth-role",
			},
		}
		for name, expected := range expectedRoleRefs {
			var actual rbacv1.RoleRef
			if rb, ok := res.roleBindings[name]; ok {
				actual = rb.RoleRef
			} else if crb, ok := res.clusterRoleBindings[name]; ok {
				actual = crb.RoleRef
			} else {
				continue
			}
			if actual.Kind != expected.Kind || actual.Name != expected.Name {
				t.Errorf(
					"Binding %q must reference %s %q, got %s %q",
					name, expected.Kind, expected.Name, actual.Kind, actual.Name,
				)
			}
		}

		errs := validateRBACRelationships(res, "controller-manager", "system")
		if len(errs) > 0 {
			for _, err := range errs {
				t.Errorf("RBAC verification error: %v", err)
			}
		}
	})

	t.Run("ValidationRejections", func(t *testing.T) {
		makeBaseResources := func() *rbacResources {
			res := newRBACResources()

			sa := &corev1.ServiceAccount{}
			sa.Name = "controller-manager"
			sa.Namespace = "system"
			res.serviceAccounts["system/controller-manager"] = sa

			role := &rbacv1.Role{}
			role.Name = "test-role"
			role.Namespace = "system"
			res.roles["system/test-role"] = role

			clusterRole := &rbacv1.ClusterRole{}
			clusterRole.Name = "test-cluster-role"
			res.clusterRoles["test-cluster-role"] = clusterRole

			rb := &rbacv1.RoleBinding{}
			rb.Name = "test-rb"
			rb.Namespace = "system"
			rb.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "test-role"}
			rb.Subjects = []rbacv1.Subject{{Kind: "ServiceAccount", Name: "controller-manager", Namespace: "system"}}
			res.roleBindings["system/test-rb"] = rb

			crb := &rbacv1.ClusterRoleBinding{}
			crb.Name = "test-crb"
			crb.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "test-cluster-role"}
			crb.Subjects = []rbacv1.Subject{{Kind: "ServiceAccount", Name: "controller-manager", Namespace: "system"}}
			res.clusterRoleBindings["test-crb"] = crb

			return res
		}

		testCases := []struct {
			name    string
			mutate  func(*rbacResources)
			wantErr string
		}{
			{
				name: "MissingServiceAccount",
				mutate: func(res *rbacResources) {
					delete(res.serviceAccounts, "system/controller-manager")
				},
				wantErr: `ServiceAccount "system/controller-manager" not found`,
			},
			{
				name: "MissingReferencedRole",
				mutate: func(res *rbacResources) {
					delete(res.roles, "system/test-role")
				},
				wantErr: `references non-existent Role`,
			},
			{
				name: "MissingReferencedClusterRole",
				mutate: func(res *rbacResources) {
					delete(res.clusterRoles, "test-cluster-role")
				},
				wantErr: `references non-existent ClusterRole`,
			},
			{
				name: "InvalidScopeForClusterRoleBinding",
				mutate: func(res *rbacResources) {
					res.clusterRoleBindings["test-crb"].RoleRef.Kind = "Role"
				},
				wantErr: `roleRef kind must be ClusterRole`,
			},
			{
				name: "UnexpectedSubjectServiceAccount",
				mutate: func(res *rbacResources) {
					res.roleBindings["system/test-rb"].Subjects[0].Name = "wrong-sa"
				},
				wantErr: `unexpected subject`,
			},
			{
				name: "AdditionalUnauthorizedSubject",
				mutate: func(res *rbacResources) {
					res.clusterRoleBindings["test-crb"].Subjects = append(res.clusterRoleBindings["test-crb"].Subjects, rbacv1.Subject{
						Kind:      "ServiceAccount",
						Name:      "extra-sa",
						Namespace: "system",
					})
				},
				wantErr: `unexpected subject`,
			},
			{
				name: "NonEmptyAPIGroupForServiceAccountSubject",
				mutate: func(res *rbacResources) {
					res.roleBindings["system/test-rb"].Subjects[0].APIGroup = "rbac.authorization.k8s.io"
				},
				wantErr: `unexpected subject`,
			},
			{
				name: "EmptySubjects",
				mutate: func(res *rbacResources) {
					res.roleBindings["system/test-rb"].Subjects = nil
				},
				wantErr: `has no subjects`,
			},
			{
				name: "InvalidRoleRefAPIGroup",
				mutate: func(res *rbacResources) {
					res.roleBindings["system/test-rb"].RoleRef.APIGroup = "invalid.group"
				},
				wantErr: `roleRef apiGroup must be`,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				res := makeBaseResources()
				tc.mutate(res)
				errs := validateRBACRelationships(res, "controller-manager", "system")
				if len(errs) == 0 {
					t.Fatalf("Expected error containing %q, got no error", tc.wantErr)
				}
				matched := false
				for _, err := range errs {
					if strings.Contains(err.Error(), tc.wantErr) {
						matched = true
						break
					}
				}
				if !matched {
					t.Errorf("Expected error containing %q, got: %v", tc.wantErr, errs)
				}
			})
		}
	})
}
