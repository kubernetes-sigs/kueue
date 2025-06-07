package jobframework

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
				if diff := cmp.Diff(got, workloadSuffix(hashLength, "a")); diff != "" {
					t.Errorf("workloadSuffix() got(-),want(+): %s", diff)
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
				if diff := cmp.Diff(got, workloadSuffix(hashLength, "a", "b")); diff != "" {
					t.Errorf("workloadSuffix() got(-),want(+): %s", diff)
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
			want: "a-b",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(workloadPrefix(tt.args.maxLength, tt.args.values...), tt.want); diff != "" {
				t.Errorf("workloadPrefix() got(-),want(+): %s", diff)
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
	tests := map[string]struct {
		args args
		want string
	}{
		"RegularInput": {
			args: args{
				ownerName: "owner",
				ownerGVK:  schema.GroupVersionKind{Kind: "kind", Group: "group"},
				ownerUID:  "uid",
			},
			want: "kind-owner" + nameDelimiter + workloadSuffix(hashLength, "kind", "group", "owner", "uid"),
		},
		"InputThatResultsInTrimmedPrefix": {
			args: args{
				// The combination of provided kind-ownerName result in value that exceeds allowed prefix length.
				ownerName: strings.Repeat("a", maxPrefixLength),
				ownerGVK:  schema.GroupVersionKind{Kind: "kind", Group: "group"},
				ownerUID:  "uid",
			},
			want: "kind" + nameDelimiter +
				strings.Repeat("a", maxPrefixLength-len("kind"+nameDelimiter)) + // Trimmed prefix.
				nameDelimiter +
				// Note: suffix value is computed using full name value.
				workloadSuffix(hashLength, "kind", "group", strings.Repeat("a", maxPrefixLength), "uid"),
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(GetWorkloadNameForOwnerWithGVK(tt.args.ownerName, tt.args.ownerUID, tt.args.ownerGVK), tt.want); diff != "" {
				t.Errorf("GetWorkloadNameForOwnerWithGVK() got(-),want(+): %s", diff)
			}
		})
	}
}

func TestWorkloadName(t *testing.T) {
	type args struct {
		owner    client.Object
		ownerGVK schema.GroupVersionKind
	}
	testName := "test-job"
	testUID := uuid.NewUUID()
	testVeryLongName := strings.Repeat("a", maxPrefixLength)

	tests := map[string]struct {
		args        args
		want        string
		doesNotWant string
	}{
		"RegularInput": {
			args: args{
				owner: &batchv1.Job{
					ObjectMeta: metav1.ObjectMeta{
						Name:       testName,
						UID:        testUID,
						Generation: 2,
					},
				},
				ownerGVK: schema.GroupVersionKind{Kind: "kind", Group: "group"},
			},
			want: // Prefix.
			"kind" + nameDelimiter + testName +
				nameDelimiter +
				// Suffix.
				workloadSuffix(hashLength, "kind", "group", testName, string(testUID), "2"),
			doesNotWant: // Prefix
			"kind" + nameDelimiter + testName +
				nameDelimiter +
				// Suffix.
				workloadSuffix(hashLength, "kind", "group", testName, string(testUID), "1"),
		},
		"InputThatResultsInTrimmedPrefix": {
			args: args{
				owner: &batchv1.Job{
					ObjectMeta: metav1.ObjectMeta{
						Name:       testVeryLongName,
						UID:        testUID,
						Generation: 2,
					},
				},
				ownerGVK: schema.GroupVersionKind{Kind: "kind", Group: "group"},
			},
			want:
			// Prefix: Since the prefix is composed of repeated characters, trimming from either end yields the same result.
			"kind" + nameDelimiter + testVeryLongName[len("kind"+nameDelimiter):] +
				nameDelimiter +
				// Suffix.
				workloadSuffix(hashLength, "kind", "group", testVeryLongName, string(testUID), "2"),
			doesNotWant: // Prefix
			"kind" + nameDelimiter + testVeryLongName[len("kind"+nameDelimiter):] +
				nameDelimiter +
				// Suffix.
				workloadSuffix(hashLength, "kind", "group", testVeryLongName, string(testUID), "1"),
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(WorkloadName(tt.args.owner, tt.args.ownerGVK), tt.want); diff != "" {
				t.Errorf("GetWorkloadNameForOwnerWithGVK() got(-),want(+): %s", diff)
			}
			if diff := cmp.Diff(WorkloadName(tt.args.owner, tt.args.ownerGVK), tt.doesNotWant); diff == "" {
				t.Errorf("GetWorkloadNameForOwnerWithGVK() expected not to be equal to:%s", tt.doesNotWant)
			}
		})
	}
}
