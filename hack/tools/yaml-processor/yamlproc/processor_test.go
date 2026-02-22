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

package yamlproc

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"go.uber.org/zap"
)

func TestApplyOperations(t *testing.T) {
	logger = zap.NewNop()
	yqClient := NewYQClient()
	textInserter := NewTextInserter(yqClient)
	fp := NewProcessor(yqClient, textInserter)

	tests := map[string]struct {
		data            string
		opType          string
		key             string
		value           string
		addKeyIfMissing bool
		onCondition     string
		indentation     int
		want            string
		wantErr         []string
	}{
		// Update cases
		"Update_SingleKey": {
			data: `
key: value
`,
			opType: Update,
			key:    ".key",
			value:  `"newValue"`,
			want: `
key: newValue
`,
		},
		"Update_NestedKey": {
			data: `
root:
  child: value
`,
			opType: Update,
			key:    ".root.child",
			value:  `"newValue"`,
			want: `
root:
  child: newValue
`,
		},

		// Append cases
		"Append_ToSimpleValue": {
			data: `
key: value1
`,
			opType: Append,
			key:    ".key",
			value:  `"suffix-"`,
			want: `
key: suffix-value1
`,
		},
		"Append_ToValuesInArray": {
			data: `
root:
  - name: one
  - name: two
`,
			opType: Append,
			key:    ".root.[].name",
			value:  `"child-"`,
			want: `
root:
  - name: child-one
  - name: child-two
`,
		},

		// InsertObject cases
		"InsertObject_SingleKey": {
			data: `
key1: value1
`,
			opType:          InsertObject,
			key:             ".key2",
			value:           `"value2"`,
			addKeyIfMissing: true,
			want: `
key1: value1
key2: value2
`,
		},
		"InsertObject_Array": {
			data: `
root:
  child1: value1
`,
			opType:          InsertObject,
			key:             ".root.child2",
			value:           `[1,2,3]`,
			addKeyIfMissing: true,
			want: `
root:
  child1: value1
  child2:
    - 1
    - 2
    - 3
`,
		},
		"InsertObject_NestedObject": {
			data: `
root:
  child:
    name: value1
`,
			opType: InsertObject,
			key:    ".root.child",
			value: `
subchild:
  name: value2
`,
			want: `
root:
  child:
    subchild:
      name: value2
    name: value1
`,
		},

		// Delete cases
		"Delete_SingleKey": {
			data: `
key1: value1
key2: value2
`,
			opType: Delete,
			key:    ".key2",
			want: `
key1: value1
`,
		},
		"Delete_NestedKey": {
			data: `
root:
  child1: value1
  child2: value2
`,
			opType: Delete,
			key:    ".root.child2",
			want: `
root:
  child1: value1
`,
		},

		// InsertText cases
		"InsertText_BelowSingleKey": {
			data: `
key1: value1
`,
			opType: InsertText,
			key:    ".key1",
			value:  "plain text",
			want: `
key1: value1
plain text
`,
		},
		"InsertText_OnConditionMet": {
			data: `
root:
  child:
    name: child1
`,
			opType:      InsertText,
			key:         ".root.child",
			value:       "plain text\n",
			indentation: 2,
			onCondition: `.root.child.name == "child1"`,
			want: `
root:
  child:
    plain text
    name: child1
`,
		},
		"InsertText_OnConditionNotMet": {
			data: `
root:
  child:
    name: child2
`,
			opType:      InsertText,
			key:         ".root.child",
			value:       "plain text\n",
			indentation: 2,
			onCondition: `.root.child.name == "child1"`,
			want: `
root:
  child:
    name: child2
`,
			wantErr: []string{
				"condition '.root.child.name == \"child1\"' not met",
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			op := Operation{
				Type:            tt.opType,
				Key:             tt.key,
				Value:           tt.value,
				AddKeyIfMissing: tt.addKeyIfMissing,
				OnFileCondition: tt.onCondition,
				Indentation:     tt.indentation,
			}
			fileOps := FileOperations{}
			if tt.opType == InsertText {
				fileOps.PostOperations = []Operation{op}
			} else {
				fileOps.Operations = []Operation{op}
			}
			got, errs := fp.ProcessFileOperations([]byte(tt.data), fileOps)
			var gotErrs []string
			for _, err := range errs {
				gotErrs = append(gotErrs, err.Error())
			}

			if diff := cmp.Diff(tt.wantErr, gotErrs); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}

			if diff := cmp.Diff(tt.want, string(got)); diff != "" {
				t.Errorf("unexpected result (-want/+got):\n%s", diff)
			}
		})
	}
}
