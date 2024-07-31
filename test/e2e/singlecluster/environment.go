package e2e

import (
	"os"
)

const (

	// Dummy sleep image to use for testing.
	SleepTestImage = "SLEEP_TEST_IMAGE"
)

func GetSleepImage() string {
	return lookupEnvOrDefault(SleepTestImage, "gcr.io/k8s-staging-perf-tests/sleep:v0.1.0")
}

func lookupEnvOrDefault(key, value string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return value
}
