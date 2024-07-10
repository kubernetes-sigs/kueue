## v0.3.2

Changes since `v0.3.1`:

### Bug or Regression

- Add permission to update frameworkjob status. (#798, @tenzen-y)
- Fix a bug where a child batch/job of an unmanaged parent (doesn't have queue name) was being suspended. (#839, @tenzen-y)
- Fix panic in cluster queue if resources and coveredResources do not have the same length. (#799, @kannon92)
- Fix: Potential over-admission within cohort when borrowing. (#822, @trasc)
- Fixed preemption to prefer preempting workloads that were more recently admitted. (#845, @stuton)

## v0.3.1

Changes since `v0.3.0`:

### Bug fixes

- Fix a bug that the validation webhook doesn't validate the queue name set as a label when creating MPIJob. #711
- Fix a bug that updates a queue name in workloads with an empty value when using framework jobs that use batch/job internally, such as MPIJob. #713
- Fix a bug in which borrowed values are set to a non-zero value even though the ClusterQueue doesn't belong to a cohort. #761
- Fixed adding suspend=true job/mpijob by the default webhook. #765

## v0.3.0

Changes since `v0.2.1`:

### Features

- Support for kubeflow's MPIJob (v2beta1)
- Upgrade the `config.kueue.x-k8s.io` API version from `v1alpha1` to `v1beta1`. `v1alpha1` is no longer supported.
  `v1beta1` includes the following changes:
  - Add `namespace` to propagate the namespace where kueue is deployed to the webhook certificate.
  - Add `internalCertManagement` with fields `enable`, `webhookServiceName` and `webhookSecretName`.
  - Remove `enableInternalCertManagement`. Use `internalCertManagement.enable` instead.
- Upgrade the `kueue.x-k8s.io` API version from `v1alpha2` to `v1beta1`.
  `v1alpha2` is no longer supported.
  `v1beta1` includes the following changes:
  - `ClusterQueue`:
    - Immutability of `spec.queueingStrategy`.
    - Refactor `quota.min` and `quota.max` into `nominalQuota` and `borrowingLimit`.
    - Swap hierarchy between `resources` and `flavors`.
    - Group flavors and resources into `spec.resourceGroups` to make
      co-dependent resources explicit.
    - Move `admission` from `spec` to `status`.
    - Add `conditions` field to `status`.
  - `LocalQueue`:
    - Add `admitted` field in `status`.
    - Add `conditions` field to `status`.
  - `Workload`:
    - Add `metadata` to `podSet` templates.
    - Move `admission` into `status`.
  - `ResourceFlavor`:
    - Introduce `spec` to hold all fields.
    - Rename `labels` to `nodeLabels`.
    - Rename `taints` to `nodeTaints`.
- Reduce API calls by setting `.status.admission` and updating the `Admitted` condition in the same API call.
- Obtain queue names from label `kueue.x-k8s.io/queue-name`. The annotation with
  the same name is still supported, but it's now deprecated.
- Multiplatform support for `linux/amd64` and `linux/arm64`.
- Validating webhook for `batch/v1.Job` validates kueue-specific labels and
  annotations.
- Sequential admission of jobs https://kueue.sigs.k8s.io/docs/tasks/setup_wait_for_pods_ready/
- Preemption within ClusterQueue and cohort https://kueue.sigs.k8s.io/docs/concepts/cluster_queue/#preemption
- Support for LimitRanges when calculating jobs usage.
- Library for integrating job-like CRDs (controller and webhooks) https://sigs.k8s.io/kueue/pkg/controller/jobframework

## Production Readiness

- E2E tests for kubernetes 1.24, 1.25 1.26 on Kind
- Improve readability and code location in logging #14
- Optimized configuration for small size clusters with higher API QPS and number
  of workers.
- Reproducible load tests https://sigs.k8s.io/kueue/test/performance
- Documentation website https://kueue.sigs.k8s.io/docs/

### Bug fixes

- Fix job controller ClusterRole for clusters that enable OwnerReferencesPermissionEnforcement admission control validation #392
- Fix race condition when admission attempt and requeuing happen at the same time #427
- Atomically release quota and requeue previously inadmissible workloads #512
- Fix support for leader election #580
- Fix support for RuntimeClass when calculating jobs usage #565
