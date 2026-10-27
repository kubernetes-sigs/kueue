<h1 align="center">
  <img src="site/static/images/logo.svg" alt="Kueue logo" width="40" height="40"> Kueue
</h1>

<p align="center">
  <strong><a href="#get-started">Get&nbsp;started</a></strong> ·
  <a href="https://kueue.sigs.k8s.io/docs/">Docs</a> ·
  <a href="#community">Community</a> ·
  <a href="#help-kueue-grow">Help&nbsp;Kueue&nbsp;grow</a>
</p>

Kueue is a Kubernetes-native queueing and scheduling system that decides **when**, **where** and **how** your training, inference and batch workloads run.

It works alongside kube-scheduler, cluster-autoscaler and your job controllers instead of replacing them.
Learn more in the [overview](https://kueue.sigs.k8s.io/docs/overview/).

## Capabilities at a glance

- **[Fair sharing](https://kueue.sigs.k8s.io/docs/concepts/fair_sharing/)** — share quota fairly across teams, with borrowing through [cohorts](https://kueue.sigs.k8s.io/docs/concepts/cohort/) and optional preemption.
- **[Hierarchical quotas](https://kueue.sigs.k8s.io/docs/concepts/cohort/#hierarchical-cohorts)** — organize quota in a tree of cohorts, such as departments and teams.
- **[Flavor fungibility](https://kueue.sigs.k8s.io/docs/concepts/cluster_queue/#flavorfungibility)** — choose when to try another [resource flavor](https://kueue.sigs.k8s.io/docs/concepts/resource_flavor/), such as spot nodes, before borrowing or preempting.
- **[Elastic workloads](https://kueue.sigs.k8s.io/docs/concepts/elastic_workload/)** — scale supported jobs up or down without suspending them, reserving quota for each scale-up.
- **[Admission checks](https://kueue.sigs.k8s.io/docs/concepts/admission_check/)** — hold workloads until external checks pass, such as node capacity from cluster-autoscaler.
- **[Device quotas (DRA)](https://kueue.sigs.k8s.io/docs/concepts/dynamic_resource_allocation/)** — admit workloads only when there is quota for the GPUs and other devices they request through DRA.
- **[All-or-nothing (gang) scheduling](https://kueue.sigs.k8s.io/docs/concepts/all_or_nothing/)** — by default, no Pods start until there is quota for all of them.
- **[Topology-Aware Scheduling (TAS)](https://kueue.sigs.k8s.io/docs/concepts/topology_aware_scheduling/)** — place communicating Pods close together, such as on one rack.
- **[MultiKueue](https://kueue.sigs.k8s.io/docs/concepts/multikueue/)** — dispatch workloads to other clusters with free capacity.

<details>
<summary><strong>Integrations:</strong> Kubernetes Job, JobSet, KubeRay, LeaderWorkerSet and more</summary>

**Built-in integrations**, grouped by common use:

- **Batch and training:** [Kubernetes Job](https://kueue.sigs.k8s.io/docs/tasks/run/jobs/) (including [CronJobs](https://kueue.sigs.k8s.io/docs/tasks/run/run_cronjobs/)), [JobSet](https://kueue.sigs.k8s.io/docs/tasks/run/jobsets/), [MPIJob](https://kueue.sigs.k8s.io/docs/tasks/run/kubeflow/mpijobs/), [RayJob](https://kueue.sigs.k8s.io/docs/tasks/run/rayjobs/) ([KubeRay](https://docs.ray.io/en/latest/cluster/kubernetes/index.html)), [TrainJob (Kubeflow Trainer v2)](https://kueue.sigs.k8s.io/docs/tasks/run/trainjobs/) and [SparkApplication](https://kueue.sigs.k8s.io/docs/tasks/run/kubeflow/sparkapplications/) (alpha).
- **Inference and serving:** [LeaderWorkerSet](https://kueue.sigs.k8s.io/docs/tasks/run/leaderworkerset/), [RayService](https://kueue.sigs.k8s.io/docs/tasks/run/rayservices/), [Deployment](https://kueue.sigs.k8s.io/docs/tasks/run/deployment/) and [StatefulSet](https://kueue.sigs.k8s.io/docs/tasks/run/statefulset/).
- **Other workloads:** [Pods and Pod groups](https://kueue.sigs.k8s.io/docs/tasks/run/plain_pods/), [AppWrapper](https://kueue.sigs.k8s.io/docs/tasks/run/appwrappers/) and [RayCluster](https://kueue.sigs.k8s.io/docs/tasks/run/rayclusters/).

**External and custom integrations:** run [external frameworks](https://kueue.sigs.k8s.io/docs/tasks/run/external_workloads/), such as [Argo Workflows](https://kueue.sigs.k8s.io/docs/tasks/run/external_workloads/argo_workflow/) and [Tekton](https://kueue.sigs.k8s.io/docs/tasks/run/external_workloads/tektoncd/), or [write an integration](https://kueue.sigs.k8s.io/docs/tasks/dev/integrate_a_custom_job/) for your own workload types.

**Deprecated:** [Kubeflow Trainer v1](https://kueue.sigs.k8s.io/docs/tasks/run/kubeflow/) jobs ([PyTorchJob](https://kueue.sigs.k8s.io/docs/tasks/run/kubeflow/pytorchjobs/), [TFJob](https://kueue.sigs.k8s.io/docs/tasks/run/kubeflow/tfjobs/), [JAXJob](https://kueue.sigs.k8s.io/docs/tasks/run/kubeflow/jaxjobs/), [PaddleJob](https://kueue.sigs.k8s.io/docs/tasks/run/kubeflow/paddlejobs/), [XGBoostJob](https://kueue.sigs.k8s.io/docs/tasks/run/kubeflow/xgboostjobs/)); migrate to [TrainJob](https://kueue.sigs.k8s.io/docs/tasks/run/trainjobs/) or [JobSet](https://kueue.sigs.k8s.io/docs/tasks/run/jobsets/).

</details>

<details>
<summary><strong>Roadmap:</strong> priorities and long-term goals</summary>

Priorities include integrating with Workload-Aware Scheduling (WAS) in kube-scheduler and continued improvements for training, inference and other workloads.

**2026 priorities**

- Integrate with Workload-Aware Scheduling (WAS) in kube-scheduler, including Topology-Aware Scheduling [#8871](https://github.com/kubernetes-sigs/kueue/issues/8871)
- [MultiKueue](https://kueue.sigs.k8s.io/docs/concepts/multikueue/) improvements:
  - Extend support for elastic Ray workloads, including autoscaling [#13243](https://github.com/kubernetes-sigs/kueue/issues/13243) (replaces [#8712](https://github.com/kubernetes-sigs/kueue/issues/8712))
  - Add workload-level admission constraints and preference-aware dispatching (proposal [#8729](https://github.com/kubernetes-sigs/kueue/issues/8729))
  - Prevent starting preemptions in multiple worker clusters [#8303](https://github.com/kubernetes-sigs/kueue/issues/8303)
  - Maintain service availability during cross-cluster moves (proposal [#8526](https://github.com/kubernetes-sigs/kueue/issues/8526))
  - Retrieve logs from worker clusters [#3526](https://github.com/kubernetes-sigs/kueue/issues/3526)
- [Topology-Aware Scheduling](https://kueue.sigs.k8s.io/docs/concepts/topology_aware_scheduling/) improvements:
  - Support resource transformations [#8860](https://github.com/kubernetes-sigs/kueue/issues/8860)
  - Extend support for [elastic workloads](https://kueue.sigs.k8s.io/docs/concepts/elastic_workload/) [#8160](https://github.com/kubernetes-sigs/kueue/issues/8160)
  - Evict workloads from nodes that become tainted [#8828](https://github.com/kubernetes-sigs/kueue/issues/8828)
- Support concurrent workload admission [#8691](https://github.com/kubernetes-sigs/kueue/issues/8691)
- Support large, high-priority jobs ("hero workloads") [#8826](https://github.com/kubernetes-sigs/kueue/issues/8826)
- Consider preemption cost when finding preemption candidates [#7990](https://github.com/kubernetes-sigs/kueue/issues/7990)
- Extend the [Dynamic Resource Allocation (DRA)](https://kueue.sigs.k8s.io/docs/concepts/dynamic_resource_allocation/) integration [#8243](https://github.com/kubernetes-sigs/kueue/issues/8243), [#2941](https://github.com/kubernetes-sigs/kueue/issues/2941)

**Long-term goals**

- Partial preemption of serving workloads [#3762](https://github.com/kubernetes-sigs/kueue/issues/3762)
- Deeper integration with workflow frameworks such as Argo Workflows and Tekton [#74](https://github.com/kubernetes-sigs/kueue/issues/74)
- Budget support [#28](https://github.com/kubernetes-sigs/kueue/issues/28)
- Flavor assignment strategies, e.g. _minimizing cost_ vs _minimizing borrowing_ [#312](https://github.com/kubernetes-sigs/kueue/issues/312)
- Cooperative preemption for workloads that implement checkpointing [#477](https://github.com/kubernetes-sigs/kueue/issues/477)
- Delayed preemption for two-stage admission [#3758](https://github.com/kubernetes-sigs/kueue/issues/3758)
- Graduate the API to v1 [#3476](https://github.com/kubernetes-sigs/kueue/issues/3476)

</details>

## Get started

Kubernetes 1.34 or newer is recommended; older versions may work but aren't tested.
Install the latest release, or see the [other installation options, including Helm](https://kueue.sigs.k8s.io/docs/getting-started/installation/):

```shell
kubectl apply --server-side -f \
  https://github.com/kubernetes-sigs/kueue/releases/download/v0.19.10/manifests.yaml
```

Then follow the **[quick start](https://kueue.sigs.k8s.io/docs/getting-started/quick-start/)** to set up resource flavors, queues and quota, and run your first job.

<details>
<summary><strong>Configuration and operations guides</strong></summary>

- **Understand queueing:** [workloads](https://kueue.sigs.k8s.io/docs/concepts/workload/), [queueing](https://kueue.sigs.k8s.io/docs/concepts/#queueing) and [admission](https://kueue.sigs.k8s.io/docs/concepts/#admission).
- **Configure queues:** [administer cluster quotas](https://kueue.sigs.k8s.io/docs/tasks/manage/administer_cluster_quotas/), then set [priorities](https://kueue.sigs.k8s.io/docs/concepts/workload/#priority) and [queueing strategies](https://kueue.sigs.k8s.io/docs/concepts/cluster_queue/#queueing-strategy).
- **Share quota:** [cohorts](https://kueue.sigs.k8s.io/docs/concepts/cohort/), [borrowing](https://kueue.sigs.k8s.io/docs/concepts/cluster_queue/#cohort), [fair-sharing policies](https://kueue.sigs.k8s.io/docs/concepts/preemption/#fair-sharing), [preemption](https://kueue.sigs.k8s.io/docs/concepts/cluster_queue/#preemption), [flavor fungibility](https://kueue.sigs.k8s.io/docs/concepts/cluster_queue/#flavorfungibility) and [dynamic reclaim](https://kueue.sigs.k8s.io/docs/concepts/workload/#dynamic-reclaim).
- **Size jobs:** start a Job smaller with [partial admission](https://kueue.sigs.k8s.io/docs/tasks/run/jobs/#partial-admission), or resize supported jobs with [elastic workloads](https://kueue.sigs.k8s.io/docs/concepts/elastic_workload/).
- **Control admission:** [admission checks](https://kueue.sigs.k8s.io/docs/concepts/admission_check/), [autoscaling with ProvisioningRequest](https://kueue.sigs.k8s.io/docs/concepts/admission_check/provisioning_request/#job-using-a-provisioningrequest), [all-or-nothing scheduling](https://kueue.sigs.k8s.io/docs/concepts/all_or_nothing/), [Pod readiness](https://kueue.sigs.k8s.io/docs/tasks/manage/setup_wait_for_pods_ready/) and [enforcing Kueue management](https://kueue.sigs.k8s.io/docs/tasks/manage/enforce_job_management/).
- **Place workloads:** set up [Topology-Aware Scheduling](https://kueue.sigs.k8s.io/docs/tasks/manage/setup_topology_aware_scheduling/) and [MultiKueue](https://kueue.sigs.k8s.io/docs/tasks/manage/setup_multikueue/).
- **Manage device quotas:** [DRA concepts](https://kueue.sigs.k8s.io/docs/concepts/dynamic_resource_allocation/) and [DRA setup](https://kueue.sigs.k8s.io/docs/tasks/manage/setup_dra/); consumable capacity is alpha.
- **Run in production:** [production setup](https://kueue.sigs.k8s.io/docs/tasks/manage/productization/) (cert-manager, network policies, Prometheus) and [RBAC](https://kueue.sigs.k8s.io/docs/tasks/manage/rbac/).
- **Monitor Kueue:** [observability](https://kueue.sigs.k8s.io/docs/tasks/manage/observability/), [metrics](https://kueue.sigs.k8s.io/docs/reference/metrics/), [pending workloads](https://kueue.sigs.k8s.io/docs/tasks/manage/monitor_pending_workloads/pending_workloads_on_demand/) and the [KueueViz dashboard](https://kueue.sigs.k8s.io/docs/tasks/manage/enable_kueueviz/).

Browse [concepts](https://kueue.sigs.k8s.io/docs/concepts/), [task guides](https://kueue.sigs.k8s.io/docs/tasks/), [examples](site/static/examples) and [DeepWiki](https://deepwiki.com/kubernetes-sigs/kueue).

</details>

## Production readiness

- Used in production; see the [public adopters](https://kueue.sigs.k8s.io/community/adopters/).
- Minor releases about every two months; see the [release process](RELEASE.md).
- Unit, integration and scalability tests, plus end-to-end tests on every [Kubernetes version Kueue officially supports](https://kueue.sigs.k8s.io/docs/getting-started/installation/#before-you-begin).

<details>
<summary><strong>Testing and compatibility</strong></summary>

**API stability:** core APIs use `v1beta2` and follow the [Kubernetes deprecation policy](https://kubernetes.io/docs/reference/using-api/deprecation-policy/).

**Testing:** the dashboards below track the `main` branch, and end-to-end suites run on Kind clusters. For supported Kubernetes versions, see the [installation prerequisites](https://kueue.sigs.k8s.io/docs/getting-started/installation/#before-you-begin); for scalability, see the [performance test setup](test/performance).

| Kubernetes version | E2E dashboards |
| :--- | :--- |
| 1.34 | [baseline](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-baseline-main-1-34) · [extended shard 0](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-extended-shard-0-main-1-34) · [extended shard 1](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-extended-shard-1-main-1-34) · [extended shard 2](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-extended-shard-2-main-1-34) |
| 1.35 | [baseline](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-baseline-main-1-35) · [extended shard 0](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-extended-shard-0-main-1-35) · [extended shard 1](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-extended-shard-1-main-1-35) · [extended shard 2](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-extended-shard-2-main-1-35) |
| 1.36 | [baseline](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-baseline-main-1-36) · [extended shard 0](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-extended-shard-0-main-1-36) · [extended shard 1](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-extended-shard-1-main-1-36) · [extended shard 2](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-extended-shard-2-main-1-36) |
| 1.37 | [baseline](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-baseline-main-1-37) · [extended shard 0](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-extended-shard-0-main-1-37) · [extended shard 1](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-extended-shard-1-main-1-37) · [extended shard 2](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-extended-shard-2-main-1-37) |

| Test suite | Dashboards |
| :--- | :--- |
| Unit | [dashboard](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-unit-main) |
| Integration | [shard 0](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-integration-shard-0-main) · [shard 1](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-integration-shard-1-main) · [shard 2](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-integration-shard-2-main) · [shard 3](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-integration-shard-3-main) · [MultiKueue](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-integration-multikueue-main) |
| TAS E2E | [baseline](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-tas-baseline-main) · [extended shard 0](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-tas-extended-shard-0-main) · [extended shard 1](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-tas-extended-shard-1-main) |
| MultiKueue E2E | [baseline](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-multikueue-baseline-main) · [extended shard 0](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-multikueue-extended-shard-0-main) · [extended shard 1](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-multikueue-extended-shard-1-main) · [DRA](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-multikueue-dra-main) |
| Sequential E2E, baseline | [shard 0](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-sequential-baseline-shard-0-main) · [shard 1](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-sequential-baseline-shard-1-main) |
| Sequential E2E, extended | [shard 0](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-sequential-extended-shard-0-main) · [shard 1](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-sequential-extended-shard-1-main) |
| Other E2E | [cert-manager](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-certmanager-main) · [DRA](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-dra-baseline-main) |
| Performance | [baseline](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-scheduling-perf-main) · [TAS](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-tas-scheduling-perf-main) · [large scale](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-large-scale-scheduling-perf-main) |

</details>

## Community

**Connect:** [WG Batch](https://github.com/kubernetes/community/tree/main/wg-batch) · [Slack](https://kubernetes.slack.com/messages/wg-batch) · [mailing list](https://groups.google.com/a/kubernetes.io/g/wg-batch) · [talks](https://kueue.sigs.k8s.io/community/talks_and_presentations/)<br>
**Contribute:** [contributing guide](CONTRIBUTING.md) · [Kubernetes community](https://kubernetes.io/community/) · [code of conduct](code-of-conduct.md)

### Help Kueue grow

Showing how people use and value Kueue helps keep the project well supported and growing:

- **[Star Kueue](https://github.com/kubernetes-sigs/kueue)** if you like the project.
- **[Add your organization](https://kueue.sigs.k8s.io/community/adopters/)** if you run Kueue in production, by editing [`site/data/adopters.yaml`](site/data/adopters.yaml).
- **[Share your experience](https://kueue.sigs.k8s.io/community/showcase/)** on Medium with the `kueue` tag.

---

[Releases](https://github.com/kubernetes-sigs/kueue/releases) · [Latest release](https://github.com/kubernetes-sigs/kueue/releases/latest) · Artwork: [Kueue](https://github.com/cncf/artwork/tree/main/projects/kubernetes/sub-projects/kueue), [KueueViz](https://github.com/cncf/artwork/tree/main/projects/kubernetes/sub-projects/kueueviz)
