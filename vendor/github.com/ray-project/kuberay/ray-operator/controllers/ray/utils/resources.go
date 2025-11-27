package utils

import (
	"regexp"
	"strings"
)

func IsGPUResourceKey(key string) bool {
	// ending with "gpu" like "nvidia.com/gpu"
	if strings.HasSuffix(key, "gpu") {
		return true
	}
	// Nvidia Multi-Instance GPU in the form of "nvidia.com/mig-<slice_count>g.<memory_size>gb" like "nvidia.com/mig-2g.32gb"
	// reference: https://github.com/NVIDIA/k8s-device-plugin#configuration-option-details
	match, _ := regexp.MatchString(`nvidia\.com/mig-\d+g\.\d+gb$`, key)
	return match
}
