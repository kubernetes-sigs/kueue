package common

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
)

// Generate a string of length 200.
func longString(t *testing.T) string {
	var b bytes.Buffer
	for i := 0; i < 200; i++ {
		b.WriteString("a")
	}
	result := b.String()
	// Confirm length.
	assert.Equal(t, len(result), 200)
	return result
}

// Clip the above string using utils.CheckName
// to a string of length 50.
func shortString(t *testing.T) string {
	result := utils.CheckName(longString(t))
	// Confirm length.
	assert.Equal(t, len(result), 50)
	return result
}
