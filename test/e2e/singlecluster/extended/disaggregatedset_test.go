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

package extended

import (
	"maps"
	"slices"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobs/disaggregatedset"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingds "sigs.k8s.io/kueue/pkg/util/testingjobs/disaggregatedset"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

// revisionEnv is the environment variable that the rollout tests change.
const revisionEnv = "REVISION"

var _ = ginkgo.Describe("DisaggregatedSet integration", ginkgo.Label("area:singlecluster", "feature:disaggregatedset"), func() {
	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
		// prefill is a role with one Pod per replica, and decode a role with a
		// leader and a worker per replica. Each Pod requests 200m CPU.
		prefill *testingds.RoleWrapper
		decode  *testingds.RoleWrapper
	)

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "ds-e2e-")
		rf = utiltestingapi.MakeResourceFlavor("ds-rf-"+ns.Name).NodeLabel("instance-type", "on-demand").Obj()
		behavioral.MustCreate(ctx, k8sClient, rf)
		// The quota fits one slice of a prefill and a decode replica: 3 Pods.
		cq = utiltestingapi.MakeClusterQueue("ds-cq-" + ns.Name).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
				Resource(corev1.ResourceCPU, "1").
				Obj()).
			Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)
		lq = utiltestingapi.MakeLocalQueue("ds-lq-"+ns.Name, ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

		prefill = testingds.MakeRole("prefill").
			Image(e2e.GetAgnHostImage()).
			Args(e2e.BehaviorWaitForDeletion...).
			RequestAndLimit(corev1.ResourceCPU, "200m").
			TerminationGracePeriod(1)
		decode = testingds.MakeRole("decode").
			WithLeader().
			Size(2).
			Image(e2e.GetAgnHostImage()).
			Args(e2e.BehaviorWaitForDeletion...).
			RequestAndLimit(corev1.ResourceCPU, "200m").
			TerminationGracePeriod(1)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteAllDisaggregatedSetsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	sliceKey := func(ds *disaggregatedsetv1.DisaggregatedSet, slice int32) types.NamespacedName {
		return types.NamespacedName{Name: disaggregatedset.GetWorkloadName(ds.UID, ds.Name, slice), Namespace: ds.Namespace}
	}

	// slicePods returns the live Pods of a DisaggregatedSet, optionally of one slice.
	slicePods := func(g gomega.Gomega, ds *disaggregatedsetv1.DisaggregatedSet, labels client.MatchingLabels) []corev1.Pod {
		selector := client.MatchingLabels{disaggregatedsetv1.SetNameLabelKey: ds.Name}
		maps.Copy(selector, labels)
		pods := &corev1.PodList{}
		g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ds.Namespace), selector)).To(gomega.Succeed())
		live := pods.Items[:0]
		for _, pod := range pods.Items {
			if pod.DeletionTimestamp.IsZero() {
				live = append(live, pod)
			}
		}
		return live
	}

	expectPodsRunning := func(ds *disaggregatedsetv1.DisaggregatedSet, count int, labels client.MatchingLabels) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			pods := slicePods(g, ds, labels)
			g.Expect(pods).To(gomega.HaveLen(count))
			for _, pod := range pods {
				g.Expect(utilpod.HasGate(&pod, podconstants.SchedulingGateName)).To(gomega.BeFalse())
				g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
			}
		}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
	}

	expectPodsGated := func(ds *disaggregatedsetv1.DisaggregatedSet, count int) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			pods := slicePods(g, ds, nil)
			g.Expect(pods).To(gomega.HaveLen(count))
			for _, pod := range pods {
				g.Expect(utilpod.HasGate(&pod, podconstants.SchedulingGateName)).To(gomega.BeTrue())
			}
		}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
	}

	expectAdmitted := func(key types.NamespacedName) *kueue.Workload {
		ginkgo.GinkgoHelper()
		wl := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
			g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
		}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
		return wl
	}

	update := func(ds *disaggregatedsetv1.DisaggregatedSet, mutate func(*disaggregatedsetv1.DisaggregatedSet)) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)).To(gomega.Succeed())
			mutate(ds)
			g.Expect(k8sClient.Update(ctx, ds)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}

	// setRevision changes every role template in a way that keeps the Pod shape,
	// which starts a rollout.
	setRevision := func(ds *disaggregatedsetv1.DisaggregatedSet, revision string) {
		ginkgo.GinkgoHelper()
		update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
			for i := range ds.Spec.Roles {
				lwt := &ds.Spec.Roles[i].Spec.LeaderWorkerTemplate
				for _, t := range []*corev1.PodTemplateSpec{lwt.LeaderTemplate, &lwt.WorkerTemplate} {
					if t != nil {
						t.Spec.Containers[0].Env = []corev1.EnvVar{{Name: revisionEnv, Value: revision}}
					}
				}
			}
		})
	}

	hasRevision := func(pod *corev1.Pod, revision string) bool {
		return slices.Contains(pod.Spec.Containers[0].Env, corev1.EnvVar{Name: revisionEnv, Value: revision})
	}

	// expectRolledOut waits until the slice Pods are all running the revision,
	// and checks that the rollout kept the admitted Workload of the slice.
	expectRolledOut := func(ds *disaggregatedsetv1.DisaggregatedSet, count int, revision string, admitted *kueue.Workload) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			pods := slicePods(g, ds, nil)
			g.Expect(pods).To(gomega.HaveLen(count))
			for _, pod := range pods {
				g.Expect(hasRevision(&pod, revision)).To(gomega.BeTrue())
				g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
			}
		}, behavioral.VeryLongTimeout, behavioral.Interval).Should(gomega.Succeed())

		wl := &kueue.Workload{}
		gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(admitted), wl)).To(gomega.Succeed())
		gomega.Expect(wl.UID).To(gomega.Equal(admitted.UID))
		gomega.Expect(workloadfinish.IsFinished(wl)).To(gomega.BeFalse())
		gomega.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
	}

	ginkgo.It("should run every role of an admitted slice and keep every role of a slice without quota gated", func() {
		running := testingds.MakeDisaggregatedSet("running", ns.Name).
			Queue(lq.Name).
			Roles(prefill.Clone(), decode.Clone()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, running)
		expectAdmitted(sliceKey(running, 0))
		expectPodsRunning(running, 3, nil)

		gated := testingds.MakeDisaggregatedSet("gated", ns.Name).
			Queue(lq.Name).
			Roles(prefill.Clone(), decode.Clone()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, gated)
		expectPodsGated(gated, 3)
		wl := &kueue.Workload{}
		gomega.Expect(k8sClient.Get(ctx, sliceKey(gated, 0), wl)).To(gomega.Succeed())
		gomega.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeFalse())
	})

	ginkgo.It("should preempt a whole slice for a higher-priority workload and re-admit it when quota frees up", func() {
		low := utiltestingapi.MakeWorkloadPriorityClass("low-" + ns.Name).PriorityValue(100).Obj()
		behavioral.MustCreate(ctx, k8sClient, low)
		high := utiltestingapi.MakeWorkloadPriorityClass("high-" + ns.Name).PriorityValue(1000).Obj()
		behavioral.MustCreate(ctx, k8sClient, high)
		ginkgo.DeferCleanup(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, low, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, high, true)
		})

		inference := testingds.MakeDisaggregatedSet("inference", ns.Name).
			Queue(lq.Name).
			WorkloadPriorityClass(low.Name).
			Roles(prefill.Clone(), decode.Clone()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, inference)
		expectAdmitted(sliceKey(inference, 0))
		expectPodsRunning(inference, 3, nil)

		training := testingds.MakeDisaggregatedSet("training", ns.Name).
			Queue(lq.Name).
			WorkloadPriorityClass(high.Name).
			Roles(prefill.Clone(), decode.Clone()).
			Obj()

		ginkgo.By("creating a higher-priority DisaggregatedSet, which preempts every role of the slice", func() {
			behavioral.MustCreate(ctx, k8sClient, training)
			gomega.Eventually(func(g gomega.Gomega) {
				wl := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, sliceKey(inference, 0), wl)).To(gomega.Succeed())
				g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadPreempted))
			}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
			expectPodsRunning(training, 3, nil)
			expectPodsGated(inference, 3)
		})

		ginkgo.By("deleting the higher-priority DisaggregatedSet, which re-admits the slice", func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, training, true)
			expectAdmitted(sliceKey(inference, 0))
			expectPodsRunning(inference, 3, nil)
		})
	})

	ginkgo.It("should create and release slices when spec.slices changes", func() {
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue(lq.Name).
			Roles(prefill.Clone()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)
		expectAdmitted(sliceKey(ds, 0))
		expectPodsRunning(ds, 1, nil)

		ginkgo.By("scaling to two slices", func() {
			update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) { ds.Spec.Slices = new(int32(2)) })
			expectAdmitted(sliceKey(ds, 1))
			expectPodsRunning(ds, 2, nil)
		})

		ginkgo.By("scaling back to one slice, which releases the quota of the removed slice", func() {
			update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) { ds.Spec.Slices = new(int32(1)) })
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(apierrors.IsNotFound(k8sClient.Get(ctx, sliceKey(ds, 1), &kueue.Workload{}))).To(gomega.BeTrue())
			}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
			expectPodsRunning(ds, 0, client.MatchingLabels{disaggregatedsetv1.SliceLabelKey: "1"})
			expectPodsRunning(ds, 1, client.MatchingLabels{disaggregatedsetv1.SliceLabelKey: "0"})
		})
	})

	ginkgo.It("should release the quota when every role is scaled to zero, and re-admit on scale-up", func() {
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue(lq.Name).
			Roles(prefill.Clone(), decode.Clone()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)
		expectAdmitted(sliceKey(ds, 0))
		expectPodsRunning(ds, 3, nil)

		ginkgo.By("scaling every role to zero", func() {
			update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
				for i := range ds.Spec.Roles {
					ds.Spec.Roles[i].Spec.Replicas = new(int32(0))
				}
			})
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(apierrors.IsNotFound(k8sClient.Get(ctx, sliceKey(ds, 0), &kueue.Workload{}))).To(gomega.BeTrue())
			}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
			expectPodsRunning(ds, 0, nil)
		})

		ginkgo.By("scaling the roles up again", func() {
			update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
				for i := range ds.Spec.Roles {
					ds.Spec.Roles[i].Spec.Replicas = new(int32(1))
				}
			})
			expectAdmitted(sliceKey(ds, 0))
			expectPodsRunning(ds, 3, nil)
		})
	})

	ginkgo.It("should roll out a new template within the admitted quota without finishing the slice", func() {
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue(lq.Name).
			Roles(prefill.Clone().Replicas(2), decode.Clone()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)
		admitted := expectAdmitted(sliceKey(ds, 0))
		expectPodsRunning(ds, 4, nil)

		setRevision(ds, "2")
		expectRolledOut(ds, 4, "2", admitted)
	})

	ginkgo.When("a rollout starts while an old group is not Ready", func() {
		const notReadyMarkerFile = "/tmp/not-ready"
		var (
			ds       *disaggregatedsetv1.DisaggregatedSet
			admitted *kueue.Workload
			// unready is the old Pod whose readiness probe fails.
			unready *corev1.Pod
		)

		setNotReady := func(pod *corev1.Pod, notReady bool) {
			ginkgo.GinkgoHelper()
			command := []string{"rm", "-f", notReadyMarkerFile}
			if notReady {
				command = []string{"touch", notReadyMarkerFile}
			}
			_, stderr, err := e2e.KExecute(ctx, cfg, restClient, pod.Namespace, pod.Name, pod.Spec.Containers[0].Name, command)
			gomega.Expect(err).To(gomega.Succeed(), string(stderr))
		}

		// expectReadyGroups waits until the DisaggregatedSet controller reports
		// the number of Ready groups of the role.
		expectReadyGroups := func(ds *disaggregatedsetv1.DisaggregatedSet, count int32) {
			ginkgo.GinkgoHelper()
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ds), ds)).To(gomega.Succeed())
				g.Expect(ds.Status.RoleStatuses).To(gomega.HaveExactElements(gomega.HaveField("ReadyReplicas", count)))
			}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
		}

		ginkgo.BeforeEach(func() {
			ds = testingds.MakeDisaggregatedSet("ds", ns.Name).
				Queue(lq.Name).
				Roles(prefill.Clone().Replicas(2).ReadinessProbe(&corev1.Probe{
					Exec: &corev1.ExecAction{
						Command: []string{"/bin/sh", "-c", "test ! -e " + notReadyMarkerFile},
					},
					PeriodSeconds:    1,
					TimeoutSeconds:   1,
					FailureThreshold: 1,
					SuccessThreshold: 1,
				})).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, ds)
			admitted = expectAdmitted(sliceKey(ds, 0))
			expectPodsRunning(ds, 2, nil)
			// A running Pod is not Ready until its first probe succeeds, and that
			// probe can still succeed after the marker file is created.
			expectReadyGroups(ds, 2)

			ginkgo.By("making one group not Ready, which uses the unavailability budget of the role", func() {
				unready = slicePods(gomega.Default, ds, nil)[0].DeepCopy()
				setNotReady(unready, true)
				// The DisaggregatedSet controller must count the group as not Ready
				// before the rollout starts, or it drains an old group first.
				expectReadyGroups(ds, 1)
			})

			ginkgo.By("changing the role template, which stalls the rollout", func() {
				setRevision(ds, "2")
				// The DisaggregatedSet controller creates an extra group of the new
				// revision and waits for it. The slice has no free slot for it, so
				// Kueue does not adopt its Pod, and no old group is drained.
				stalled := func(g gomega.Gomega) {
					var oldPods, newPods []corev1.Pod
					for _, pod := range slicePods(g, ds, nil) {
						if hasRevision(&pod, "2") {
							newPods = append(newPods, pod)
						} else {
							oldPods = append(oldPods, pod)
						}
					}
					g.Expect(oldPods).To(gomega.HaveLen(2))
					g.Expect(newPods).To(gomega.HaveLen(1))
					g.Expect(utilpod.HasGate(&newPods[0], podconstants.SchedulingGateName)).To(gomega.BeTrue())
					g.Expect(newPods[0].Labels).NotTo(gomega.HaveKey(constants.ManagedByKueueLabelKey))
				}
				gomega.Eventually(stalled, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
				gomega.Consistently(stalled, behavioral.LongConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should continue the rollout after the group is Ready again", func() {
			setNotReady(unready, false)
			expectRolledOut(ds, 2, "2", admitted)
		})

		ginkgo.It("should complete the rollout after maxUnavailable is raised by the group that cannot recover", func() {
			update(ds, func(ds *disaggregatedsetv1.DisaggregatedSet) {
				strategy := &ds.Spec.Roles[0].Spec.RolloutStrategy
				if strategy.RollingUpdateConfiguration == nil {
					strategy.RollingUpdateConfiguration = &leaderworkersetv1.RollingUpdateConfiguration{}
				}
				strategy.RollingUpdateConfiguration.MaxUnavailable = intstr.FromInt32(2)
			})
			expectRolledOut(ds, 2, "2", admitted)
		})
	})

	// The quota fits both DisaggregatedSets: 4 Pods.
	ginkgo.It("should start a role with a leader and a worker", func() {
		ginkgo.By("starting a role with the LeaderCreated startup policy", func() {
			ds := testingds.MakeDisaggregatedSet("leader-created", ns.Name).
				Queue(lq.Name).
				Roles(decode.Clone().StartupPolicy(leaderworkersetv1.LeaderCreatedStartupPolicy)).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, ds)
			expectAdmitted(sliceKey(ds, 0))
			expectPodsRunning(ds, 2, nil)
		})

		ginkgo.By("starting a role with the LeaderReady startup policy", func() {
			ds := testingds.MakeDisaggregatedSet("leader-ready", ns.Name).
				Queue(lq.Name).
				Roles(decode.Clone().StartupPolicy(leaderworkersetv1.LeaderReadyStartupPolicy)).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, ds)
			expectAdmitted(sliceKey(ds, 0))
			expectPodsRunning(ds, 2, nil)
		})
	})

	ginkgo.It("should delete the Workloads with the DisaggregatedSet", func() {
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue(lq.Name).
			Roles(prefill.Clone()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)
		wl := expectAdmitted(sliceKey(ds, 0))
		expectPodsRunning(ds, 1, nil)

		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, ds, true)
		behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, wl, false, behavioral.LongTimeout)
	})
})
