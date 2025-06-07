package jobframework

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/workload/workloadslicing"
)

var testJobGVK = batchv1.SchemeGroupVersion.WithKind("Job")

type mockJob struct {
	object       *batchv1.Job
	suspended    bool
	podSets      []kueue.PodSet
	podSetsError error
	gvk          schema.GroupVersionKind
}

func (j *mockJob) Object() client.Object {
	return j.object
}

func (j *mockJob) IsSuspended() bool {
	return j.suspended
}

func (j *mockJob) Suspend() {}

func (j *mockJob) RunWithPodSetsInfo(_ []podset.PodSetInfo) error {
	panic("implement me")
}

func (j *mockJob) RestorePodSetsInfo(_ []podset.PodSetInfo) bool {
	panic("implement me")
}

func (j *mockJob) Finished() (message string, success, finished bool) {
	panic("implement me")
}

func (j *mockJob) PodSets() ([]kueue.PodSet, error) {
	return j.podSets, j.podSetsError
}

func (j *mockJob) IsActive() bool {
	panic("implement me")
}

func (j *mockJob) PodsReady() bool {
	panic("implement me")
}

func (j *mockJob) GVK() schema.GroupVersionKind { return j.gvk }

func testJobObject(optIn bool) *batchv1.Job {
	job := &batchv1.Job{}
	if optIn {
		metav1.SetMetaDataAnnotation(&job.ObjectMeta, workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue)
	}
	return job
}

func TestJobReconciler_ensureWorkload(t *testing.T) {
	type fields struct {
		client client.Client
		record record.EventRecorder

		manageJobsWithoutQueueName   bool
		managedJobsNamespaceSelector labels.Selector
		waitForPodsReady             bool
		labelKeysToCopy              []string
		clock                        clock.Clock
		workloadRetentionPolicy      WorkloadRetentionPolicy
	}
	type args struct {
		ctx context.Context
		job GenericJob
	}
	type want struct {
		err      bool
		workload *kueue.Workload
	}
	testClient := func() *fake.ClientBuilder {
		sch := runtime.NewScheme()
		_ = kueue.AddToScheme(sch)
		return fake.NewClientBuilder().
			WithScheme(sch).
			WithIndex(&kueue.Workload{}, indexer.IndexWorkloadOwnerKey(testJobGVK), indexer.IndexWorkloadOwner(testJobGVK))
	}
	tests := map[string]struct {
		workloadSliceEnabled bool
		fields               fields
		args                 args
		want                 want
	}{
		"WorkloadSliceNotEnabled_Failure": {
			args: args{
				ctx: t.Context(),
				job: &mockJob{object: testJobObject(false)},
			},
			fields: fields{
				client: fake.NewClientBuilder().Build(),
			},
			want: want{err: true},
		},
		"WorkloadSliceNotEnabled_Success": {
			args: args{
				ctx: t.Context(),
				job: &mockJob{
					object:    testJobObject(false),
					gvk:       testJobGVK,
					suspended: true,
				},
			},
			fields: fields{
				client: testClient().Build(),
			},
		},
		"WorkloadSliceEnabled_NoPreviousWorkload": {
			workloadSliceEnabled: true,
			args: args{
				ctx: t.Context(),
				job: &mockJob{
					object: testJobObject(true),
					gvk:    testJobGVK,
				},
			},
			fields: fields{
				client: testClient().Build(),
			},
		},
		"WorkloadSliceEnabled_FailureRetrievingJobPodSets": {
			workloadSliceEnabled: true,
			args: args{
				ctx: t.Context(),
				job: &mockJob{
					object:       testJobObject(true),
					gvk:          testJobGVK,
					podSetsError: errors.New("test-get-podsets-error"),
				},
			},
			want: want{
				err: true,
			},
		},
		"WorkloadSliceEnabled_FailureRetrievingWorkloads": {
			workloadSliceEnabled: true,
			args: args{
				ctx: t.Context(),
				job: &mockJob{
					object: testJobObject(true),
					gvk:    testJobGVK,
				},
			},
			fields: fields{
				client: testClient().
					WithInterceptorFuncs(interceptor.Funcs{
						List: func(ctx context.Context, client client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
							return errors.New("test-list-workload-error")
						},
					}).
					Build(),
			},
			want: want{
				err: true,
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			r := &JobReconciler{
				client:                       tt.fields.client,
				record:                       tt.fields.record,
				manageJobsWithoutQueueName:   tt.fields.manageJobsWithoutQueueName,
				managedJobsNamespaceSelector: tt.fields.managedJobsNamespaceSelector,
				waitForPodsReady:             tt.fields.waitForPodsReady,
				labelKeysToCopy:              tt.fields.labelKeysToCopy,
				clock:                        tt.fields.clock,
				workloadRetentionPolicy:      tt.fields.workloadRetentionPolicy,
			}
			if err := features.SetEnable(features.WorkloadSlices, tt.workloadSliceEnabled); err != nil {
				t.Errorf("ensureWorkload() unexpected error enabling feature: %v", err)
			}

			got, err := r.ensureWorkload(tt.args.ctx, tt.args.job, tt.args.job.Object())
			if (err != nil) != tt.want.err {
				t.Errorf("ensureWorkload() error = %v, wantErr %v", err, tt.want.err)
				return
			}
			if diff := cmp.Diff(got, tt.want.workload); diff != "" {
				t.Errorf("ensureWorkload() got(-),want(+): %s", diff)
			}
		})
	}
}
