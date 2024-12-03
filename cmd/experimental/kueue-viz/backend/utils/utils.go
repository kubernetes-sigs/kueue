package utils

import (
	"log"
	"time"
)

// MeasureTime measures the execution time of a function, logs it, and returns both the result and error
func MeasureTime(name string, f func() (interface{}, error)) (interface{}, error) {
	start := time.Now()

	// Run the provided function
	result, err := f()

	// Log the duration
	duration := time.Since(start)
	log.Printf("[%s] executed in %v", name, duration)

	return result, err
}
