package pod

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	kubeflow "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	rayjobapi "github.com/ray-project/kuberay/ray-operator/apis/ray/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingutil "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestDefault(t *testing.T) {
	testCases := map[string]struct {
		initObjects                []client.Object
		pod                        *corev1.Pod
		manageJobsWithoutQueueName bool
		namespaceSelector          *metav1.LabelSelector
		podSelector                *metav1.LabelSelector
		want                       *corev1.Pod
		wantError                  error
	}{
		"pod with queue nil ns selector": {
			initObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-ns",
						Labels: map[string]string{
							"kubernetes.io/metadata.name": "test-ns",
						},
					},
				},
			},
			pod: testingutil.MakePod("test-pod", "test-ns").
				Queue("test-queue").
				Obj(),
			want: testingutil.MakePod("test-pod", "test-ns").
				Queue("test-queue").
				Obj(),
		},
		"pod with queue matching ns selector": {
			initObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-ns",
						Labels: map[string]string{
							"kubernetes.io/metadata.name": "test-ns",
						},
					},
				},
			},
			pod: testingutil.MakePod("test-pod", "test-ns").
				Queue("test-queue").
				Obj(),
			namespaceSelector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      "kubernetes.io/metadata.name",
						Operator: metav1.LabelSelectorOpNotIn,
						Values:   []string{"kube-system"},
					},
				},
			},
			podSelector: &metav1.LabelSelector{},
			want: testingutil.MakePod("test-pod", "test-ns").
				Queue("test-queue").
				SetLabel("kueue.x-k8s.io/managed", "true").
				KueueSchedulingGate().
				KueueFinalizer().
				Obj(),
		},
		"pod without queue matching ns selector manage jobs without queue name": {
			initObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-ns",
						Labels: map[string]string{
							"kubernetes.io/metadata.name": "test-ns",
						},
					},
				},
			},
			pod: testingutil.MakePod("test-pod", "test-ns").
				Obj(),
			manageJobsWithoutQueueName: true,
			namespaceSelector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      "kubernetes.io/metadata.name",
						Operator: metav1.LabelSelectorOpNotIn,
						Values:   []string{"kube-system"},
					},
				},
			},
			podSelector: &metav1.LabelSelector{},
			want: testingutil.MakePod("test-pod", "test-ns").
				SetLabel("kueue.x-k8s.io/managed", "true").
				KueueSchedulingGate().
				KueueFinalizer().
				Obj(),
		},
		"pod with owner managed by kueue (Job)": {
			initObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-ns",
						Labels: map[string]string{
							"kubernetes.io/metadata.name": "test-ns",
						},
					},
				},
			},
			pod: testingutil.MakePod("test-pod", "test-ns").
				Queue("test-queue").
				OwnerReference("parent-job", batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
			want: testingutil.MakePod("test-pod", "test-ns").
				Queue("test-queue").
				OwnerReference("parent-job", batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
		},
		"pod with owner managed by kueue (RayCluster)": {
			initObjects: []client.Object{
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-ns",
						Labels: map[string]string{
							"kubernetes.io/metadata.name": "test-ns",
						},
					},
				},
			},
			pod: testingutil.MakePod("test-pod", "test-ns").
				Queue("test-queue").
				OwnerReference("parent-ray-cluster", rayjobapi.GroupVersion.WithKind("RayCluster")).
				Obj(),
			want: testingutil.MakePod("test-pod", "test-ns").
				Queue("test-queue").
				OwnerReference("parent-ray-cluster", rayjobapi.GroupVersion.WithKind("RayCluster")).
				Obj(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			builder := utiltesting.NewClientBuilder(kubeflow.AddToScheme)
			builder = builder.WithObjects(tc.initObjects...)
			cli := builder.Build()

			w := &PodWebhook{
				client:                     cli,
				manageJobsWithoutQueueName: tc.manageJobsWithoutQueueName,
				namespaceSelector:          tc.namespaceSelector,
				podSelector:                tc.podSelector,
			}

			ctx, _ := utiltesting.ContextWithLog(t)

			if err := w.Default(ctx, tc.pod); err != nil {
				t.Errorf("failed to set defaults to a v1/pod: %s", err)
			}
			if diff := cmp.Diff(tc.want, tc.pod); len(diff) != 0 {
				t.Errorf("Default() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}
