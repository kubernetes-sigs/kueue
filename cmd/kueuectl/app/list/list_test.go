package list

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericiooptions"

	"sigs.k8s.io/kueue/client-go/clientset/versioned/fake"
	cmdtesting "sigs.k8s.io/kueue/cmd/kueuectl/app/testing"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestListCmd(t *testing.T) {
	testStartTime := time.Now()

	testCases := map[string]struct {
		ns         string
		objs       []runtime.Object
		args       []string
		wantOut    string
		wantOutErr string
		wantErr    error
	}{
		"should print local queue list with all namespaces": {
			objs: []runtime.Object{
				utiltesting.MakeLocalQueue("lq1", "ns1").
					ClusterQueue("cq1").
					PendingWorkloads(1).
					AdmittedWorkloads(1).
					Creation(testStartTime.Add(-1 * time.Hour).Truncate(time.Second)).
					Obj(),
				utiltesting.MakeLocalQueue("lq2", "ns2").
					ClusterQueue("cq2").
					PendingWorkloads(2).
					AdmittedWorkloads(2).
					Creation(testStartTime.Add(-2 * time.Hour).Truncate(time.Second)).
					Obj(),
			},
			args: []string{"--all-namespaces"},
			wantOut: `NAMESPACE   NAME   CLUSTERQUEUE   PENDING WORKLOADS   ADMITTED WORKLOADS   AGE
ns1         lq1    cq1            1                   1                    60m
ns2         lq2    cq2            2                   2                    120m
`,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			streams, _, out, outErr := genericiooptions.NewTestIOStreams()

			tf := cmdtesting.NewTestClientGetter()
			if len(tc.ns) > 0 {
				tf.WithNamespace(tc.ns)
			} else {
				tf.WithNamespace(defaultNamespace)
			}

			tf.ClientSet = fake.NewSimpleClientset(tc.objs...)

			cmd := NewLocalQueueCmd(tf, streams)
			cmd.SetArgs(tc.args)

			gotErr := cmd.Execute()
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}

			gotOut := out.String()
			if diff := cmp.Diff(tc.wantOut, gotOut); diff != "" {
				t.Errorf("Unexpected output (-want/+got)\n%s", diff)
			}

			gotOutErr := outErr.String()
			if diff := cmp.Diff(tc.wantOutErr, gotOutErr); diff != "" {
				t.Errorf("Unexpected output (-want/+got)\n%s", diff)
			}
		})
	}
}
