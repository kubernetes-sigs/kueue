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

package finish

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

var errTest = errors.New("test error")

func TestFinish(t *testing.T) {
	const (
		baseReason  = "TestReason"
		baseMessage = "Test Message"
	)

	now := time.Now().Truncate(time.Second)

	baseWl := utiltestingapi.MakeWorkload("wl", metav1.NamespaceDefault).ResourceVersion("1")

	type args struct {
		wl       *kueue.Workload
		reason   string
		message  string
		patchErr error
	}

	type want struct {
		wl  *kueue.Workload
		err error
	}

	tests := map[string]struct {
		args args
		want want
	}{
		"finish workload": {
			args: args{
				wl:      baseWl.DeepCopy(),
				reason:  baseReason,
				message: baseMessage,
			},
			want: want{
				wl: baseWl.Clone().
					ResourceVersion("2").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadFinished,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.NewTime(now),
						Reason:             baseReason,
						Message:            baseMessage,
					}).
					Obj(),
			},
		},
		"already finished workload": {
			args: args{
				wl:      baseWl.Clone().FinishedAt(now).Obj(),
				reason:  "OtherReason",
				message: "Other Message",
			},
			want: want{
				wl: baseWl.Clone().FinishedAt(now).Obj(),
			},
		},
		"error on finish": {
			args: args{
				wl:       baseWl.DeepCopy(),
				reason:   baseReason,
				message:  baseMessage,
				patchErr: errTest,
			},
			want: want{
				wl:  baseWl.DeepCopy(),
				err: errTest,
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)

			cl := utiltesting.NewClientBuilder().
				WithObjects(tc.args.wl).
				WithStatusSubresource(&kueue.Workload{}).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
						if tc.args.patchErr != nil {
							return tc.args.patchErr
						}
						return utiltesting.TreatSSAAsStrategicMerge(ctx, c, subResourceName, obj, patch, opts...)
					},
				}).
				Build()

			fakeClock := testingclock.NewFakeClock(now)

			gotErr := Finish(ctx, cl, tc.args.wl, tc.args.reason, tc.args.message, fakeClock)
			if diff := cmp.Diff(tc.want.err, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}

			updatedWl := &kueue.Workload{}
			if err := cl.Get(ctx, client.ObjectKeyFromObject(tc.args.wl), updatedWl); err != nil {
				t.Fatalf("Failed obtaining updated object: %v", err)
			}

			if diff := cmp.Diff(tc.want.wl, updatedWl, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected workload (-want,+got):\n%s", diff)
			}
		})
	}
}
