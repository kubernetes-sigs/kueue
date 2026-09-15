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

package e2e

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var (
	sparkTestImageOnce sync.Once
	sparkTestImage     string

	agnHostImageOnce sync.Once
	agnHostImage     string

	redisTestImageOnce sync.Once
	redisTestImage     string
)

const (
	// Update these if the default images change
	defaultE2eTestAgnHostImageOld = "registry.k8s.io/e2e-test-images/agnhost:2.26"
)

// GetAgnHostImageOld returns the old agnhost image for backwards compatibility testing
func GetAgnHostImageOld() string {
	if image := os.Getenv("E2E_TEST_AGNHOST_IMAGE_OLD"); image != "" {
		return image
	}
	return defaultE2eTestAgnHostImageOld
}

// GetAgnHostImage returns the agnhost test image
func GetAgnHostImage() string {
	return getCachedDockerImage(&agnHostImageOnce, &agnHostImage, "E2E_TEST_AGNHOST_IMAGE", "agnhost")
}

// GetSparkTestImage returns the Spark test image
func GetSparkTestImage() string {
	return getCachedDockerImage(&sparkTestImageOnce, &sparkTestImage, "E2E_TEST_SPARK_IMAGE", "spark")
}

// GetRedisTestImage returns the Redis test image
func GetRedisTestImage() string {
	return getCachedDockerImage(&redisTestImageOnce, &redisTestImage, "E2E_TEST_REDIS_IMAGE", "redis")
}

// GetKuberayTestImage returns the KubeRay test image
func GetKuberayTestImage() string {
	kuberayTestImage, found := os.LookupEnv("KUBERAY_RAY_IMAGE")
	gomega.Expect(found).To(gomega.BeTrue())
	return kuberayTestImage
}

// GetClusterProfilePluginImage returns the ClusterProfile plugin image
func GetClusterProfilePluginImage() string {
	ginkgo.GinkgoHelper()
	clusterProfilePluginImage, found := os.LookupEnv("CLUSTERPROFILE_PLUGIN_IMAGE")
	gomega.Expect(found).To(gomega.BeTrue())
	return clusterProfilePluginImage
}

// VersionFromImage extracts the version tag from an image reference
func VersionFromImage(image string) string {
	if at := strings.IndexByte(image, '@'); at != -1 {
		image = image[:at]
	}

	if colon := strings.LastIndexByte(image, ':'); colon > strings.LastIndexByte(image, '/') {
		return image[colon+1:]
	}
	return ""
}

// getCachedDockerImage resolves a test image exactly once via the supplied sync.Once,
// caching the result in *cache. It returns the value of envVar if set, otherwise the
// image parsed from hack/testing/<dir>/Dockerfile.
func getCachedDockerImage(once *sync.Once, cache *string, envVar, dir string) string {
	once.Do(func() {
		if image := os.Getenv(envVar); image != "" {
			*cache = image
			return
		}

		dockerfilePath := filepath.Join(ProjectBaseDir, "hack", "testing", dir, "Dockerfile")
		image, err := getDockerImageFromDockerfile(dockerfilePath)
		if err != nil {
			panic(fmt.Errorf("failed to get %s image: %w", dir, err))
		}

		*cache = image
	})
	return *cache
}

func getDockerImageFromDockerfile(filePath string) (string, error) {
	// Open the Dockerfile
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open Dockerfile: %w", err)
	}
	defer file.Close()

	// Read the file line by line
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Skip empty lines or comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Check for FROM instruction
		if strings.HasPrefix(strings.ToUpper(line), "FROM ") {
			// Extract the part after "FROM "
			parts := strings.Fields(line)
			if len(parts) < 2 {
				return "", fmt.Errorf("invalid FROM instruction: %s", line)
			}
			// The image name is the second field (parts[1])
			return parts[1], nil
		}
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("error reading Dockerfile: %w", err)
	}

	return "", errors.New("no FROM instruction found in Dockerfile")
}
