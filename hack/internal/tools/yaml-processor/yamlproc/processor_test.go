package yamlproc

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestApplyOperations(t *testing.T) {
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
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got := fp.ApplyOperations([]byte(tt.data), FileOperations{
				Operations: []Operation{
					{
						Type:            tt.opType,
						Key:             tt.key,
						Value:           tt.value,
						AddKeyIfMissing: tt.addKeyIfMissing,
						OnCondition:     tt.onCondition,
						Indentation:     tt.indentation,
					},
				},
			})

			if diff := cmp.Diff(tt.want, string(got)); diff != "" {
				t.Errorf("unexpected result (-want/+got):\n%s", diff)
			}
		})
	}
}
