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

package pod

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	ctrlconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	lwsconstants "sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset/constants"
	lwsworkloadname "sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset/workloadname"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingmpijob "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/paddlejob"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	testingpytorchjob "sigs.k8s.io/kueue/pkg/util/testingjobs/pytorchjob"
	testingtfjob "sigs.k8s.io/kueue/pkg/util/testingjobs/tfjob"
	testingxgboostjob "sigs.k8s.io/kueue/pkg/util/testingjobs/xgboostjob"

	_ "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs"
	_ "sigs.k8s.io/kueue/pkg/controller/jobs/mpijob"
	_ "sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
)

func TestDefault(t *testing.T) {
	defaultNamespace := utiltesting.MakeNamespaceWrapper("test-ns").Label(corev1.LabelMetadataName, "test-ns").Obj()
	defaultNamespaceSelector := &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{
				Key:      corev1.LabelMetadataName,
				Operator: metav1.LabelSelectorOpNotIn,
				Values:   []string{"kube-system"},
			},
		},
	}
	defaultPodSelector := &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{
				Key:      corev1.LabelMetadataName,
				Operator: metav1.LabelSelectorOpNotIn,
				Values:   []string{"kube-system"},
			},
		},
	}
	defaultManagedJobsNamespaceSelector, err := metav1.LabelSelectorAsSelector(defaultNamespaceSelector)
	if err != nil {
		t.Fatalf("failed to parse namespace selector")
	}

	testCases := map[string]struct {
		featureGates                 map[featuregate.Feature]bool
		initObjects                  []client.Object
		pod                          *corev1.Pod
		defaultLqExist               bool
		manageJobsWithoutQueueName   bool
		managedJobsNamespaceSelector labels.Selector
		namespaceSelector            *metav1.LabelSelector
		podSelector                  *metav1.LabelSelector
		enableIntegrations           []string
		want                         *corev1.Pod
		wantErr                      error
	}{
		"pod with suspend by parent annotation should skip finalizer": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			initObjects: []client.Object{defaultNamespace},
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				SuspendedByParent("test").
				Queue("test-queue").
				Obj(),
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				SuspendedByParent("test").
				Queue("test-queue").
				KueueSchedulingGate().
				RoleHash("a9f06f3a").
				Obj(),
		},
		"pod with queue nil ns selector": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			initObjects:  []client.Object{defaultNamespace},
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				Obj(),
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				ManagedByKueueLabel().
				KueueSchedulingGate().
				RoleHash("a9f06f3a").
				KueueFinalizer().
				Obj(),
		},
		"pod with queue matching ns selector": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			initObjects:  []client.Object{defaultNamespace},
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				Obj(),
			namespaceSelector: defaultNamespaceSelector,
			podSelector:       &metav1.LabelSelector{},
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				ManagedByKueueLabel().
				KueueSchedulingGate().
				RoleHash("a9f06f3a").
				KueueFinalizer().
				Obj(),
		},
		"pod without queue matching ns selector manage jobs without queue name": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			initObjects:  []client.Object{defaultNamespace},
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Obj(),
			manageJobsWithoutQueueName: true,
			namespaceSelector:          defaultNamespaceSelector,
			podSelector:                &metav1.LabelSelector{},
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				ManagedByKueueLabel().
				KueueSchedulingGate().
				RoleHash("a9f06f3a").
				KueueFinalizer().
				Obj(),
		},
		"pod with owner managed by kueue (Job) while not enabled": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			initObjects: []client.Object{
				defaultNamespace,
				testingjob.MakeJob("parent-job", defaultNamespace.Name).UID("parent-job").Queue("test-queue").Obj(),
			},
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference("parent-job", batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				ManagedByKueueLabel().
				KueueSchedulingGate().
				RoleHash("a9f06f3a").
				KueueFinalizer().
				OwnerReference("parent-job", batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
		},
		"pod with owner managed by kueue (Job)": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			initObjects: []client.Object{
				defaultNamespace,
				testingjob.MakeJob("parent-job", defaultNamespace.Name).UID("parent-job").Queue("test-queue").Obj(),
			},
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference("parent-job", batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
			enableIntegrations: []string{"batch/job"},
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference("parent-job", batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
		},
		"pod with owner managed by kueue (RayCluster)": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			initObjects: []client.Object{
				defaultNamespace,
				&rayv1.RayCluster{
					ObjectMeta: metav1.ObjectMeta{
						UID:       types.UID("parent-ray-cluster"),
						Name:      "parent-ray-cluster",
						Namespace: defaultNamespace.Name,
						Labels: map[string]string{
							ctrlconstants.QueueLabel: "test-queue",
						},
					},
				},
			},
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference("parent-ray-cluster", rayv1.GroupVersion.WithKind("RayCluster")).
				Obj(),
			enableIntegrations: []string{"ray.io/raycluster"},
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference("parent-ray-cluster", rayv1.GroupVersion.WithKind("RayCluster")).
				Obj(),
		},
		"pod with owner managed by kueue (MPIJob)": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			initObjects: []client.Object{
				defaultNamespace,
				testingmpijob.MakeMPIJob("parent-mpi-job", defaultNamespace.Name).UID("parent-mpi-job").Queue("test-queue").Obj(),
			},
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference(
					"parent-mpi-job",
					schema.GroupVersionKind{Group: "kubeflow.org", Version: "v2beta1", Kind: "MPIJob"},
				).
				Obj(),
			enableIntegrations: []string{"kubeflow.org/mpijob"},
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference(
					"parent-mpi-job",
					schema.GroupVersionKind{Group: "kubeflow.org", Version: "v2beta1", Kind: "MPIJob"},
				).
				Obj(),
		},
		"pod with owner managed by kueue (PyTorchJob)": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			initObjects: []client.Object{
				defaultNamespace,
				testingpytorchjob.MakePyTorchJob("parent-pytorch-job", defaultNamespace.Name).UID("parent-pytorch-job").Queue("test-queue").Obj(),
			},
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference(
					"parent-pytorch-job",
					schema.GroupVersionKind{Group: "kubeflow.org", Version: "v1", Kind: "PyTorchJob"},
				).
				Obj(),
			enableIntegrations: []string{"kubeflow.org/pytorchjob"},
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference(
					"parent-pytorch-job",
					schema.GroupVersionKind{Group: "kubeflow.org", Version: "v1", Kind: "PyTorchJob"},
				).
				Obj(),
		},
		"pod with owner managed by kueue (TFJob)": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			initObjects: []client.Object{
				defaultNamespace,
				testingtfjob.MakeTFJob("parent-tf-job", defaultNamespace.Name).UID("parent-tf-job").Queue("test-queue").Obj(),
			},
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference(
					"parent-tf-job",
					schema.GroupVersionKind{Group: "kubeflow.org", Version: "v1", Kind: "TFJob"},
				).
				Obj(),
			enableIntegrations: []string{"kubeflow.org/tfjob"},
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference(
					"parent-tf-job",
					schema.GroupVersionKind{Group: "kubeflow.org", Version: "v1", Kind: "TFJob"},
				).
				Obj(),
		},
		"pod with owner managed by kueue (XGBoostJob)": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			initObjects: []client.Object{
				defaultNamespace,
				testingxgboostjob.MakeXGBoostJob("parent-xgboost-job", defaultNamespace.Name).UID("parent-xgboost-job").Queue("test-queue").Obj(),
			},
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference(
					"parent-xgboost-job",
					schema.GroupVersionKind{Group: "kubeflow.org", Version: "v1", Kind: "XGBoostJob"},
				).
				Obj(),
			enableIntegrations: []string{"kubeflow.org/xgboostjob"},
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference(
					"parent-xgboost-job",
					schema.GroupVersionKind{Group: "kubeflow.org", Version: "v1", Kind: "XGBoostJob"},
				).
				Obj(),
		},
		"pod with owner managed by kueue (PaddleJob)": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			initObjects: []client.Object{
				defaultNamespace,
				paddlejob.MakePaddleJob("parent-paddle-job", defaultNamespace.Name).UID("parent-paddle-job").Queue("test-queue").Obj(),
			},
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference(
					"parent-paddle-job",
					schema.GroupVersionKind{Group: "kubeflow.org", Version: "v1", Kind: "PaddleJob"},
				).
				Obj(),
			enableIntegrations: []string{"kubeflow.org/paddlejob"},
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				OwnerReference(
					"parent-paddle-job",
					schema.GroupVersionKind{Group: "kubeflow.org", Version: "v1", Kind: "PaddleJob"},
				).
				Obj(),
		},
		"pod with a group name label, but without group total count label": {
			featureGates:      map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			initObjects:       []client.Object{defaultNamespace},
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				GroupNameLabel("test-group").
				Obj(),
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				GroupNameLabel("test-group").
				RoleHash("a9f06f3a").
				ManagedByKueueLabel().
				KueueSchedulingGate().
				KueueFinalizer().
				Obj(),
		},
		"pod with a group name label": {
			featureGates:      map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			initObjects:       []client.Object{defaultNamespace},
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				GroupNameLabel("test-group").
				Obj(),
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				GroupNameLabel("test-group").
				RoleHash("a9f06f3a").
				ManagedByKueueLabel().
				KueueSchedulingGate().
				KueueFinalizer().
				Obj(),
		},
		"pod with TAS": {
			featureGates:      map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
			initObjects:       []client.Object{defaultNamespace},
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				Annotation(kueue.PodSetRequiredTopologyAnnotation, "block").
				Obj(),
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				Annotation(kueue.PodSetRequiredTopologyAnnotation, "block").
				ManagedByKueueLabel().
				KueueFinalizer().
				RoleHash("a9f06f3a").
				KueueSchedulingGate().
				TopologySchedulingGate().
				Obj(),
		},
		"pod with TAS and PodGroupPodIndexLabelAnnotation": {
			featureGates:      map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
			initObjects:       []client.Object{defaultNamespace},
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				Label("test-label", "test-value").
				Annotation(kueue.PodGroupPodIndexLabelAnnotation, "test-label").
				Annotation(kueue.PodSetRequiredTopologyAnnotation, "block").
				Obj(),
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("test-queue").
				Annotation(kueue.PodGroupPodIndexLabelAnnotation, "test-label").
				Annotation(kueue.PodSetRequiredTopologyAnnotation, "block").
				Label("test-label", "test-value").
				Label(kueue.PodGroupPodIndexLabel, "test-value").
				ManagedByKueueLabel().
				RoleHash("a9f06f3a").
				KueueFinalizer().
				KueueSchedulingGate().
				TopologySchedulingGate().
				Obj(),
		},
		"LWS leader pod with TAS gets workload annotation and podset label": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
			initObjects: []client.Object{
				defaultNamespace,
				&leaderworkersetv1.LeaderWorkerSet{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-lws",
						Namespace: defaultNamespace.Name,
						UID:       types.UID("test-lws-uid"),
					},
					Spec: leaderworkersetv1.LeaderWorkerSetSpec{
						LeaderWorkerTemplate: leaderworkersetv1.LeaderWorkerTemplate{
							LeaderTemplate: &corev1.PodTemplateSpec{},
							WorkerTemplate: corev1.PodTemplateSpec{},
						},
					},
				},
			},
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-lws-0", defaultNamespace.Name).
				Queue("test-queue").
				Annotation(kueue.PodSetRequiredTopologyAnnotation, "block").
				// SetNameLabelKey identifies this as an LWS pod.
				Label(leaderworkersetv1.SetNameLabelKey, "test-lws").
				// Leader pods may not have GroupIndexLabelKey yet; fall back to StatefulSet ordinal.
				Label(appsv1.PodIndexLabel, "0").
				Obj(),
			want: func() *corev1.Pod {
				wlName := lwsworkloadname.Get(types.UID("test-lws-uid"), "test-lws", "0")
				return testingpod.MakePod("test-lws-0", defaultNamespace.Name).
					Queue("test-queue").
					Annotation(kueue.PodSetRequiredTopologyAnnotation, "block").
					Annotation(kueue.WorkloadAnnotation, wlName).
					Label(leaderworkersetv1.SetNameLabelKey, "test-lws").
					Label(appsv1.PodIndexLabel, "0").
					Label(constants.PodSetLabel, lwsconstants.LeaderPodSetName).
					ManagedByKueueLabel().
					KueueFinalizer().
					RoleHash("a9f06f3a").
					KueueSchedulingGate().
					TopologySchedulingGate().
					Obj()
			}(),
		},
		"LWS worker pod with TAS gets workload annotation and worker podset label": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
			initObjects: []client.Object{
				defaultNamespace,
				&leaderworkersetv1.LeaderWorkerSet{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-lws",
						Namespace: defaultNamespace.Name,
						UID:       types.UID("test-lws-uid"),
					},
					Spec: leaderworkersetv1.LeaderWorkerSetSpec{
						LeaderWorkerTemplate: leaderworkersetv1.LeaderWorkerTemplate{
							LeaderTemplate: &corev1.PodTemplateSpec{},
							WorkerTemplate: corev1.PodTemplateSpec{},
						},
					},
				},
			},
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-lws-0-1", defaultNamespace.Name).
				Queue("test-queue").
				Annotation(kueue.PodSetRequiredTopologyAnnotation, "block").
				// Workers carry the leader-name annotation.
				Annotation(leaderworkersetv1.LeaderPodNameAnnotationKey, "test-lws-0").
				Label(leaderworkersetv1.SetNameLabelKey, "test-lws").
				Label(leaderworkersetv1.GroupIndexLabelKey, "0").
				Obj(),
			want: func() *corev1.Pod {
				wlName := lwsworkloadname.Get(types.UID("test-lws-uid"), "test-lws", "0")
				return testingpod.MakePod("test-lws-0-1", defaultNamespace.Name).
					Queue("test-queue").
					Annotation(kueue.PodSetRequiredTopologyAnnotation, "block").
					Annotation(leaderworkersetv1.LeaderPodNameAnnotationKey, "test-lws-0").
					Annotation(kueue.WorkloadAnnotation, wlName).
					Label(leaderworkersetv1.SetNameLabelKey, "test-lws").
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					Label(constants.PodSetLabel, lwsconstants.WorkerPodSetName).
					ManagedByKueueLabel().
					KueueFinalizer().
					RoleHash("a9f06f3a").
					KueueSchedulingGate().
					TopologySchedulingGate().
					Obj()
			}(),
		},
		"LWS pod without LeaderTemplate does not get podset label": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
			initObjects: []client.Object{
				defaultNamespace,
				&leaderworkersetv1.LeaderWorkerSet{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-lws",
						Namespace: defaultNamespace.Name,
						UID:       types.UID("test-lws-uid"),
					},
					Spec: leaderworkersetv1.LeaderWorkerSetSpec{
						LeaderWorkerTemplate: leaderworkersetv1.LeaderWorkerTemplate{
							// No LeaderTemplate — all pods share a single podset.
							WorkerTemplate: corev1.PodTemplateSpec{},
						},
					},
				},
			},
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-lws-0", defaultNamespace.Name).
				Queue("test-queue").
				Annotation(kueue.PodSetRequiredTopologyAnnotation, "block").
				Label(leaderworkersetv1.SetNameLabelKey, "test-lws").
				Label(leaderworkersetv1.GroupIndexLabelKey, "0").
				Obj(),
			want: func() *corev1.Pod {
				wlName := lwsworkloadname.Get(types.UID("test-lws-uid"), "test-lws", "0")
				return testingpod.MakePod("test-lws-0", defaultNamespace.Name).
					Queue("test-queue").
					Annotation(kueue.PodSetRequiredTopologyAnnotation, "block").
					Annotation(kueue.WorkloadAnnotation, wlName).
					Label(leaderworkersetv1.SetNameLabelKey, "test-lws").
					Label(leaderworkersetv1.GroupIndexLabelKey, "0").
					// No PodSetLabel — single podset mode.
					ManagedByKueueLabel().
					KueueFinalizer().
					RoleHash("a9f06f3a").
					KueueSchedulingGate().
					TopologySchedulingGate().
					Obj()
			}(),
		},
		"default queue is created, pod has no queue label": {
			featureGates:      map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			initObjects:       []client.Object{defaultNamespace},
			defaultLqExist:    true,
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Obj(),
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("default").
				ManagedByKueueLabel().
				KueueSchedulingGate().
				RoleHash("a9f06f3a").
				KueueFinalizer().
				Obj(),
		},
		"default queue isn't created, pod has no queue label": {
			featureGates:      map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			initObjects:       []client.Object{defaultNamespace},
			defaultLqExist:    false,
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Obj(),
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Obj(),
		},
		"default queue is created, pod has queue label": {
			featureGates:      map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			initObjects:       []client.Object{defaultNamespace},
			defaultLqExist:    true,
			podSelector:       &metav1.LabelSelector{},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("queue").
				Obj(),
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("queue").
				ManagedByKueueLabel().
				KueueSchedulingGate().
				RoleHash("a9f06f3a").
				KueueFinalizer().
				Obj(),
		},
		"the namespace matches the selector": {
			featureGates:                 map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			initObjects:                  []client.Object{defaultNamespace},
			managedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("queue").
				Obj(),
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("queue").
				ManagedByKueueLabel().
				KueueSchedulingGate().
				RoleHash("a9f06f3a").
				KueueFinalizer().
				Obj(),
		},
		"doesn’t match the managedJobsNamespaceSelector": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			initObjects: []client.Object{
				utiltesting.MakeNamespaceWrapper("kube-system").Label(corev1.LabelMetadataName, "kube-system").Obj(),
			},
			managedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
			pod: testingpod.MakePod("test-pod", "kube-system").
				Queue("queue").
				Obj(),
			want: testingpod.MakePod("test-pod", "kube-system").
				Queue("queue").
				Obj(),
		},
		"the namespace matches the namespace selector": {
			featureGates:      map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			initObjects:       []client.Object{defaultNamespace},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("queue").
				Obj(),
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Queue("queue").
				ManagedByKueueLabel().
				RoleHash("a9f06f3a").
				KueueSchedulingGate().
				KueueFinalizer().
				Obj(),
		},
		"the namespace doesn't match the namespace selector": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			initObjects: []client.Object{
				utiltesting.MakeNamespaceWrapper("kube-system").Label(corev1.LabelMetadataName, "kube-system").Obj(),
			},
			namespaceSelector: defaultNamespaceSelector,
			pod: testingpod.MakePod("test-pod", "kube-system").
				Queue("queue").
				Obj(),
			want: testingpod.MakePod("test-pod", "kube-system").
				Queue("queue").
				Obj(),
		},
		"the pod matches the pod selector": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			initObjects:  []client.Object{defaultNamespace},
			podSelector:  defaultPodSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Label(corev1.LabelMetadataName, "test-pod").
				Queue("queue").
				Obj(),
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Label(corev1.LabelMetadataName, "test-pod").
				Queue("queue").
				ManagedByKueueLabel().
				RoleHash("a9f06f3a").
				KueueSchedulingGate().
				KueueFinalizer().
				Obj(),
		},
		"the pod doesn't match the pod selector": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			initObjects:  []client.Object{defaultNamespace},
			podSelector:  defaultPodSelector,
			pod: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Label(corev1.LabelMetadataName, "kube-system").
				Queue("queue").
				Obj(),
			want: testingpod.MakePod("test-pod", defaultNamespace.Name).
				Label(corev1.LabelMetadataName, "kube-system").
				Queue("queue").
				Obj(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			t.Cleanup(jobframework.EnableIntegrationsForTest(t, tc.enableIntegrations...))
			builder := utiltesting.NewClientBuilder(rayv1.AddToScheme, kfmpi.AddToScheme, kftraining.AddToScheme, appsv1.AddToScheme, leaderworkersetv1.AddToScheme)
			builder = builder.WithObjects(tc.initObjects...)
			cli := builder.Build()

			cqCache := schdcache.New(cli)
			queueManager := qcache.NewManagerForUnitTests(cli, cqCache)

			ctx, _ := utiltesting.ContextWithLog(t)

			if tc.defaultLqExist {
				if err := queueManager.AddLocalQueue(ctx, utiltestingapi.MakeLocalQueue("default", defaultNamespace.Name).
					ClusterQueue("cluster-queue").Obj()); err != nil {
					t.Fatalf("failed to create default local queue: %s", err)
				}
			}

			w := &PodWebhook{
				client:                       cli,
				queues:                       queueManager,
				manageJobsWithoutQueueName:   tc.manageJobsWithoutQueueName,
				managedJobsNamespaceSelector: tc.managedJobsNamespaceSelector,
				namespaceSelector:            tc.namespaceSelector,
				podSelector:                  tc.podSelector,
			}

			if err := w.Default(ctx, tc.pod); err != nil {
				t.Errorf("failed to set defaults for v1/pod: %s", err)
			}

			if diff := cmp.Diff(tc.want, tc.pod); len(diff) != 0 {
				t.Errorf("Pod mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestGetRoleHash(t *testing.T) {
	testCases := map[string]struct {
		pods []*Pod
		// If true, hash for all the pods in test should be equal
		wantEqualHash bool
		wantErr       error
	}{
		"kueue.x-k8s.io/* labels shouldn't affect the role": {
			pods: []*Pod{
				{pod: *testingpod.MakePod("pod1", "test-ns").
					ManagedByKueueLabel().
					Obj()},
				{pod: *testingpod.MakePod("pod2", "test-ns").
					Obj()},
			},
			wantEqualHash: true,
		},
		"volume name shouldn't affect the role": {
			pods: []*Pod{
				{pod: *testingpod.MakePod("pod1", "test-ns").
					Volume(corev1.Volume{
						Name: "volume1",
					}).
					Obj()},
				{pod: *testingpod.MakePod("pod1", "test-ns").
					Volume(corev1.Volume{
						Name: "volume2",
					}).
					Obj()},
			},
			wantEqualHash: true,
		},
		// NOTE: volumes used to be included in the role hash.
		// https://github.com/kubernetes-sigs/kueue/issues/1697
		"volumes with different claims shouldn't affect the role": {
			pods: []*Pod{
				{pod: *testingpod.MakePod("pod1", "test-ns").
					Volume(corev1.Volume{
						Name: "volume",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: "claim1",
							},
						},
					}).
					Obj()},
				{pod: *testingpod.MakePod("pod1", "test-ns").
					Volume(corev1.Volume{
						Name: "volume",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: "claim2",
							},
						},
					}).
					Obj()},
			},
			wantEqualHash: true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			var previousHash string
			for i := range tc.pods {
				hash, err := getRoleHash(tc.pods[i].pod)

				if diff := cmp.Diff(tc.wantErr, err); diff != "" {
					t.Errorf("Unexpected error (-want,+got):\n%s", diff)
				}

				if previousHash != "" {
					if tc.wantEqualHash {
						if previousHash != hash {
							t.Errorf("Hash of pod shapes shouldn't be different %s!=%s", previousHash, hash)
						}
					} else {
						if previousHash == hash {
							t.Errorf("Hash of pod shapes shouldn't be equal %s==%s", previousHash, hash)
						}
					}
				}

				previousHash = hash
			}
		})
	}
}

func TestDefaultReturnsErrorIfLWSCannotBeFoundForTAS(t *testing.T) {
	defaultNamespace := utiltesting.MakeNamespaceWrapper("test-ns").Label(corev1.LabelMetadataName, "test-ns").Obj()
	pod := testingpod.MakePod("test-lws-0", defaultNamespace.Name).
		Queue("test-queue").
		Annotation(kueue.PodSetRequiredTopologyAnnotation, "block").
		Label(leaderworkersetv1.SetNameLabelKey, "missing-lws").
		Label(leaderworkersetv1.GroupIndexLabelKey, "0").
		Obj()

	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{features.TopologyAwareScheduling: true})
	builder := utiltesting.NewClientBuilder(appsv1.AddToScheme, leaderworkersetv1.AddToScheme).
		WithObjects(defaultNamespace)
	cli := builder.Build()

	queueManager := qcache.NewManagerForUnitTests(cli, schdcache.New(cli))
	ctx, _ := utiltesting.ContextWithLog(t)
	w := &PodWebhook{
		client: cli,
		queues: queueManager,
	}

	err := w.Default(ctx, pod)
	if err == nil {
		t.Fatalf("Expected Default to fail when referenced LeaderWorkerSet does not exist")
	}
	if !apierrors.IsNotFound(err) {
		t.Fatalf("Expected NotFound error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "looking up LeaderWorkerSet test-ns/missing-lws") {
		t.Fatalf("Unexpected error message: %v", err)
	}
}

func TestValidateCreate(t *testing.T) {
	t.Cleanup(jobframework.EnableIntegrationsForTest(t, "batch/job"))
	testCases := map[string]struct {
		pod       *corev1.Pod
		wantErr   error
		wantWarns admission.Warnings
	}{
		"pod owner is managed by kueue": {
			pod: testingpod.MakePod("test-pod", "test-ns").
				ManagedByKueueLabel().
				OwnerReference("parent-job", batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
			wantWarns: admission.Warnings{
				"pod owner is managed by kueue, label 'kueue.x-k8s.io/managed=true' might lead to unexpected behaviour",
			},
		},
		"pod with group name and no group total count": {
			pod: testingpod.MakePod("test-pod", "test-ns").
				ManagedByKueueLabel().
				GroupNameLabel("test-group").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "metadata.annotations[kueue.x-k8s.io/pod-group-total-count]",
				},
			}.ToAggregate(),
		},
		"pod with group total count and no group name": {
			pod: testingpod.MakePod("test-pod", "test-ns").
				ManagedByKueueLabel().
				GroupTotalCount("3").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "metadata.labels[kueue.x-k8s.io/pod-group-name]",
				},
			}.ToAggregate(),
		},
		"pod with 0 group total count": {
			pod: testingpod.MakePod("test-pod", "test-ns").
				ManagedByKueueLabel().
				GroupNameLabel("test-group").
				GroupTotalCount("0").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.annotations[kueue.x-k8s.io/pod-group-total-count]",
				},
			}.ToAggregate(),
		},
		"pod with empty group total count": {
			pod: testingpod.MakePod("test-pod", "test-ns").
				ManagedByKueueLabel().
				GroupNameLabel("test-group").
				GroupTotalCount("").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.annotations[kueue.x-k8s.io/pod-group-total-count]",
				},
			}.ToAggregate(),
		},
		"pod with incorrect group name": {
			pod: testingpod.MakePod("test-pod", "test-ns").
				ManagedByKueueLabel().
				GroupNameLabel("notAdns1123Subdomain*").
				GroupTotalCount("2").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.labels[kueue.x-k8s.io/pod-group-name]",
				},
			}.ToAggregate(),
		},
		"valid topology request": {
			pod: testingpod.MakePod("test-pod", "test-ns").
				ManagedByKueueLabel().
				Annotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				Obj(),
		},
		"invalid topology request": {
			pod: testingpod.MakePod("test-pod", "test-ns").
				ManagedByKueueLabel().
				Annotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				Annotation(kueue.PodSetPreferredTopologyAnnotation, "cloud.com/block").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.annotations",
				},
			}.ToAggregate(),
		},
		"prebuilt workload for pod": {
			pod: testingpod.MakePod("test-pod", "test-ns").
				PrebuiltWorkloadLabel("workload-name").
				Obj(),
		},
		"prebuilt workload for pod group valid": {
			pod: testingpod.MakePod("test-pod", "test-ns").
				PrebuiltWorkloadLabel("group-name").
				GroupNameLabel("group-name").
				GroupTotalCount("3").
				Obj(),
		},
		"prebuilt workload for pod group invalid": {
			pod: testingpod.MakePod("test-pod", "test-ns").
				PrebuiltWorkloadLabel("workload-name").
				GroupNameLabel("group-name").
				GroupTotalCount("3").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.labels[kueue.x-k8s.io/prebuilt-workload-name]",
				},
			}.ToAggregate(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			builder := utiltesting.NewClientBuilder()
			cli := builder.Build()

			w := &PodWebhook{
				client: cli,
			}

			ctx, _ := utiltesting.ContextWithLog(t)

			warns, err := w.ValidateCreate(ctx, tc.pod)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(warns, tc.wantWarns); diff != "" {
				t.Errorf("Expected different list of warnings (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateUpdate(t *testing.T) {
	t.Cleanup(jobframework.EnableIntegrationsForTest(t, "batch/job"))
	testCases := map[string]struct {
		oldPod    *corev1.Pod
		newPod    *corev1.Pod
		wantErr   error
		wantWarns admission.Warnings
	}{
		"pods owner is managed by kueue, managed label is set for both pods": {
			oldPod: testingpod.MakePod("test-pod", "test-ns").
				ManagedByKueueLabel().
				OwnerReference("parent-job", batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
			newPod: testingpod.MakePod("test-pod", "test-ns").
				ManagedByKueueLabel().
				OwnerReference("parent-job", batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
			wantWarns: admission.Warnings{
				"pod owner is managed by kueue, label 'kueue.x-k8s.io/managed=true' might lead to unexpected behaviour",
			},
		},
		"pod owner is managed by kueue, managed label is set for new pod": {
			oldPod: testingpod.MakePod("test-pod", "test-ns").
				OwnerReference("parent-job", batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
			newPod: testingpod.MakePod("test-pod", "test-ns").
				ManagedByKueueLabel().
				OwnerReference("parent-job", batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
			wantWarns: admission.Warnings{
				"pod owner is managed by kueue, label 'kueue.x-k8s.io/managed=true' might lead to unexpected behaviour",
			},
		},
		"pod owner is managed by kueue, managed label is set for new pod with suspend by parent annotation": {
			oldPod: testingpod.MakePod("test-pod", "test-ns").
				OwnerReference("parent-job", batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
			newPod: testingpod.MakePod("test-pod", "test-ns").
				ManagedByKueueLabel().
				OwnerReference("parent-job", batchv1.SchemeGroupVersion.WithKind("Job")).
				Annotation(podconstants.SuspendedByParentAnnotation, "job").
				Obj(),
		},
		"pod with group name and no group total count": {
			oldPod: testingpod.MakePod("test-pod", "test-ns").GroupNameLabel("test-group").Obj(),
			newPod: testingpod.MakePod("test-pod", "test-ns").
				ManagedByKueueLabel().
				GroupNameLabel("test-group").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeRequired,
					Field: "metadata.annotations[kueue.x-k8s.io/pod-group-total-count]",
				},
			}.ToAggregate(),
		},
		"pod with 0 group total count": {
			oldPod: testingpod.MakePod("test-pod", "test-ns").GroupNameLabel("test-group").Obj(),
			newPod: testingpod.MakePod("test-pod", "test-ns").
				ManagedByKueueLabel().
				GroupNameLabel("test-group").
				GroupTotalCount("0").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.annotations[kueue.x-k8s.io/pod-group-total-count]",
				},
			}.ToAggregate(),
		},
		"pod with empty group total count": {
			oldPod: testingpod.MakePod("test-pod", "test-ns").GroupNameLabel("test-group").Obj(),
			newPod: testingpod.MakePod("test-pod", "test-ns").
				ManagedByKueueLabel().
				GroupNameLabel("test-group").
				GroupTotalCount("").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.annotations[kueue.x-k8s.io/pod-group-total-count]",
				},
			}.ToAggregate(),
		},
		"pod group name is changed": {
			oldPod: testingpod.MakePod("test-pod", "test-ns").
				GroupNameLabel("test-group").
				GroupTotalCount("2").
				Obj(),
			newPod: testingpod.MakePod("test-pod", "test-ns").
				GroupNameLabel("test-group-new").
				GroupTotalCount("2").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.labels[kueue.x-k8s.io/pod-group-name]",
				},
			}.ToAggregate(),
		},
		"assign pod group name": {
			oldPod: testingpod.MakePod("test-pod", "test-ns").
				Obj(),
			newPod: testingpod.MakePod("test-pod", "test-ns").
				GroupNameLabel("test-group-new").
				GroupTotalCount("2").
				Obj(),
		},
		"retriable in group annotation is removed": {
			oldPod: testingpod.MakePod("test-pod", "test-ns").
				GroupNameLabel("test-group").
				GroupTotalCount("2").
				Annotation(podconstants.RetriableInGroupAnnotationKey, podconstants.RetriableInGroupAnnotationValue).
				Obj(),
			newPod: testingpod.MakePod("test-pod", "test-ns").
				GroupNameLabel("test-group").
				GroupTotalCount("2").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeForbidden,
					Field: "metadata.annotations[kueue.x-k8s.io/retriable-in-group]",
				},
			}.ToAggregate(),
		},
		"retriable in group annotation is changed from false to true": {
			oldPod: testingpod.MakePod("test-pod", "test-ns").
				GroupNameLabel("test-group").
				GroupTotalCount("2").
				Annotation(podconstants.RetriableInGroupAnnotationKey, podconstants.RetriableInGroupAnnotationValue).
				Obj(),
			newPod: testingpod.MakePod("test-pod", "test-ns").
				GroupNameLabel("test-group").
				GroupTotalCount("2").
				Annotation(podconstants.RetriableInGroupAnnotationKey, "true").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeForbidden,
					Field: "metadata.annotations[kueue.x-k8s.io/retriable-in-group]",
				},
			}.ToAggregate(),
		},
		"prebuilt workload for pod group invalid": {
			oldPod: testingpod.MakePod("test-pod", "test-ns").
				PrebuiltWorkloadLabel("group-name").
				GroupNameLabel("group-name").
				GroupTotalCount("3").
				KueueSchedulingGate().
				Obj(),
			newPod: testingpod.MakePod("test-pod", "test-ns").
				PrebuiltWorkloadLabel("group-name-new").
				GroupNameLabel("group-name").
				GroupTotalCount("3").
				KueueSchedulingGate().
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.labels[kueue.x-k8s.io/prebuilt-workload-name]",
				},
			}.ToAggregate(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			builder := utiltesting.NewClientBuilder()
			cli := builder.Build()

			w := &PodWebhook{
				client: cli,
			}

			ctx, _ := utiltesting.ContextWithLog(t)

			warns, err := w.ValidateUpdate(ctx, tc.oldPod, tc.newPod)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(warns, tc.wantWarns); diff != "" {
				t.Errorf("Expected different list of warnings (-want,+got):\n%s", diff)
			}
		})
	}
}
