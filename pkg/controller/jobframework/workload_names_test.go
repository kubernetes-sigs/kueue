package jobframework

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

func Test_workloadSuffix(t *testing.T) {
	type args struct {
		maxLength uint
		values    []string
	}
	tests := map[string]struct {
		args args
		// Delegate result assertion to a function when dealing with "random" output.
		want func(*testing.T, string)
	}{
		"EdgeCase_ZeroLength": {
			args: args{
				maxLength: 0,
				values:    []string{"a", "b", "c"},
			},
			want: func(t *testing.T, got string) {
				if got != "" {
					t.Errorf("workloadSuffix() expected an empty string, got: %s", got)
				}
			},
		},
		"EmptyValues": {
			args: args{
				maxLength: hashLength,
			},
			want: func(t *testing.T, got string) {
				if got == "" {
					t.Errorf("workloadSuffix() expected a non-empty string")
				}
				// Assert random output for empty values list.
				if got == workloadSuffix(hashLength) {
					t.Errorf("workloadSuffix() expected a random value when provided empty values list")
				}
			},
		},
		"SingleValue": {
			args: args{
				maxLength: hashLength,
				values:    []string{"a"},
			},
			want: func(t *testing.T, got string) {
				if got == "" {
					t.Errorf("workloadSuffix() expected a non-empty string")
				}
				// Assert consistent output for the same values.
				if diff := cmp.Diff(workloadSuffix(hashLength, "a"), got); diff != "" {
					t.Errorf("workloadSuffix() want(-),got(+): %s", diff)
				}
			},
		},
		"MultipleValues": {
			args: args{
				maxLength: hashLength,
				values:    []string{"a", "b"},
			},
			want: func(t *testing.T, got string) {
				if got == "" {
					t.Errorf("workloadSuffix() expected a non-empty string")
				}
				// Assert consistent output for the same values.
				if diff := cmp.Diff(workloadSuffix(hashLength, "a", "b"), got); diff != "" {
					t.Errorf("workloadSuffix() want(-),got(+): %s", diff)
				}
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			tt.want(t, workloadSuffix(tt.args.maxLength, tt.args.values...))
		})
	}
}

func Test_workloadPrefix(t *testing.T) {
	type args struct {
		maxLength uint
		values    []string
	}
	tests := map[string]struct {
		args args
		want string
	}{
		"EdgeCase_EmptyValues": {
			args: args{
				maxLength: 10,
			},
			want: "",
		},
		"EdgeCase_ZeroMaxLength": {
			args: args{
				values: []string{"a", "b", "c"},
			},
		},
		"NotTrimmed": {
			args: args{
				maxLength: 3,
				values:    []string{"a", "b"},
			},
			want: "a-b",
		},
		"Trimmed": {
			args: args{
				maxLength: 4,
				values:    []string{"a", "b", "c"},
			},
			want: "a-b-", // Note: dangling delimiter.
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, workloadPrefix(tt.args.maxLength, tt.args.values...)); diff != "" {
				t.Errorf("workloadPrefix() want(-),got(+): %s", diff)
			}
		})
	}
}

func TestGetWorkloadNameForOwnerWithGVK(t *testing.T) {
	type args struct {
		ownerName string
		ownerUID  types.UID
		ownerGVK  schema.GroupVersionKind
	}
	// getHash is a legacy implementation used for pseudo-random suffix generation,
	// retained temporarily to verify consistency between the previous and updated behavior.
	//
	// Note: This function is slated for removal in a follow-up PR.
	getHash := func(ownerName string, ownerUID types.UID, gvk schema.GroupVersionKind) string {
		h := sha1.New()
		h.Write([]byte(gvk.Kind))
		h.Write([]byte("\n"))
		h.Write([]byte(gvk.Group))
		h.Write([]byte("\n"))
		h.Write([]byte(ownerName))
		h.Write([]byte("\n"))
		h.Write([]byte(ownerUID))
		return hex.EncodeToString(h.Sum(nil))
	}
	// longOwnerName simulates a name whose length exceeds maxPrefixLength and triggers prefix trimming.
	longOwnerName := strings.Repeat("a", maxPrefixLength)

	tests := map[string]struct {
		args       args
		want       string
		wantLegacy string
	}{
		"RegularInput": {
			args: args{
				ownerName: "owner",
				ownerGVK:  schema.GroupVersionKind{Kind: "kind", Group: "group"},
				ownerUID:  "uid",
			},
			want:       "kind-owner-" + workloadSuffix(hashLength, "kind", "group", "owner", "uid"),
			wantLegacy: "kind-owner-" + getHash("owner", "uid", schema.GroupVersionKind{Kind: "kind", Group: "group"})[:hashLength],
		},
		"InputThatResultsInTrimmedPrefix": {
			args: args{
				// The combination of provided kind-ownerName result in value that exceeds allowed prefix length.
				ownerName: longOwnerName,
				ownerGVK:  schema.GroupVersionKind{Kind: "kind", Group: "group"},
				ownerUID:  "uid",
			},
			want: "kind-" +
				longOwnerName[len("kind-"):] + // Trimmed prefix (since the prefix is a repeated character, trimming from either end yields the same result).
				nameDelimiter +
				// Note: suffix value is computed using full name value.
				workloadSuffix(hashLength, "kind", "group", longOwnerName, "uid"),
			wantLegacy: "kind-" +
				longOwnerName[len("kind-"):] + // Trimmed prefix (same as above).
				nameDelimiter +
				getHash(longOwnerName, "uid", schema.GroupVersionKind{Kind: "kind", Group: "group"})[:hashLength],
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got := GetWorkloadNameForOwnerWithGVK(tt.args.ownerName, tt.args.ownerUID, tt.args.ownerGVK)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("GetWorkloadNameForOwnerWithGVK() name(-),got(+): %s", diff)
			}
			if diff := cmp.Diff(tt.wantLegacy, got); diff != "" {
				t.Errorf("GetWorkloadNameForOwnerWithGVK() legacy-name(-),got(+): %s", diff)
			}
		})
	}
}
