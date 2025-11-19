package taints

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestTaintKeyExists(t *testing.T) {
	testingTaints := []corev1.Taint{
		{
			Key:    "foo_1",
			Value:  "bar_1",
			Effect: corev1.TaintEffectNoExecute,
		},
		{
			Key:    "foo_2",
			Value:  "bar_2",
			Effect: corev1.TaintEffectNoSchedule,
		},
	}

	cases := []struct {
		name            string
		taintKeyToMatch string
		expectedResult  bool
	}{
		{
			name:            "taint key exists",
			taintKeyToMatch: "foo_1",
			expectedResult:  true,
		},
		{
			name:            "taint key does not exist",
			taintKeyToMatch: "foo_3",
			expectedResult:  false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result := TaintKeyExists(testingTaints, c.taintKeyToMatch)

			if result != c.expectedResult {
				t.Errorf("[%s] unexpected results: %v", c.name, result)
			}
		})
	}
}
