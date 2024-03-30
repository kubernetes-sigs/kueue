## v0.5.3

Changes since `v0.5.2`:

## Changes by Kind

### Bug or Regression

- Avoid finished Workloads from blocking quota after a Kueue restart (#1699, @trasc)
- Do not (re)create ProvReq if the state of admission check is Ready (#1620, @mimowo)
- Fix Kueue crashing at the log level 6 when re-admitting workloads (#1645, @mimowo)
- Kueue replicas are advertised as Ready only once the webhooks are functional.
  
  This allows users to wait with the first requests until the Kueue deployment is available, so that the early requests don't fail. (#1682 #1713, @mimowo @trasc)
- Remove deleted pending workloads from the cache (#1687, @astefanutti)

## v0.5.2

Changes since `v0.5.1`:

### Bug or Regression

- Add Missing RBAC on integration finalizers sub-resources (#1486, @astefanutti)
- Added event for QuotaReserved and fixed event for Admitted to trigger when admission checks complete (#1436, @trasc)
- Avoid recreating a Workload for a finished Job and finalize a job when the workload is declared finished. (#1572, @alculquicondor)
- Fix a bug in the pod integration where a Workload can be left with a finalizer when a pod is not found. (#1524, @achernevskii)
- Remove finalizer from Workloads that are orphaned (have no owners). (#1523, @achernevskii, @woehrl01, @trasc)
- Add Mutating WebhookConfigurations for the AdmissionCheck, RayJob, and JobSet to helm charts (#1570, @B1F030)
- Add Validating/Mutating WebhookConfigurations for the KubeflowJobs like PyTorchJob (#1462, @tenzen-y)
- Add events for transitions of the provisioning AdmissionCheck (#1394, @stuton)
- Support for retry of provisioning request. (#1595, @mimowo)
- Webhooks are served in non-leading replicas (#1511, @astefanutti)

## v0.5.1

Changes since `v0.5.0`:

### Bug or Regression

- Fix client-go libraries bug that can not operate clusterScoped resources like ClusterQueue and ResourceFlavor. (#1294, @tenzen-y)
- Fixed fungiblity policy `whenCanPreempt: Preempt`. The admission should happen in the flavor for which preemptions were issued. (#1332, @alculquicondor)
- Fix a bug that plain pods managed by kueue will remain a terminating condition forever. (#1342, @tenzen-y)
- Fix fungibility policy `Preempt` where it was not able to utilize the next flavor if preemption was not possible. (#1366, @alculquicondor, @KunWuLuan)

## v0.5.0

Changes since `v0.4.0`:

## Changes by Kind

### Feature

- A mechanism for AdmissionChecks to provide labels, annotations, tolerations and node selectors to the pod templates when starting a job (#1180, @mimowo)
- A reference standalone controller that can be used to support plain Pods using taints and tolerations, which can be used in Kubernetes versions that don't support scheduling gates. (#1111, @nstogner)
- Add Active condition to AdmissionChecks (#1193, @trasc)
- Add optional cluster queue resource quota and usage metrics. (#982, @trasc)
- Add support for AdmissionChecks, a mechanism for internal or external components to influence whether a Workload can be admitted. (#1045, @trasc)
- Add support for single plain Pods. (#1072, @achernevskii)
- Add support for workload Priority (#1081, @Gekko0114)
- Add tolerations to ResourceFlavor. Kueue injects these tolerations to the jobs that are assigned to the flavor when admitted. (#1248, @trasc)
- Added pprof endpoints for profiling (#978, @stuton)
- Allow the admission of multiple workloads within one scheduling cycle while borrowing. (#1039, @trasc)
- An option to synchronize batch/job.completions with parallelism in case of partial admission (#971, @trasc)
- Expose cluster queue information about pending workloads (#1069, @stuton)
- Expose probe configurations to helm chart (#986, @yyzxw)
- Graduate Partial admission to Beta. (#1221, @trasc)
- Integrate with Cluster Autoscaler's ProvisioningRequest via two stage admission (#1154, @trasc)
- Manage cluster queue active state based on admission checks life cycle. (#1079, @trasc)
- Metrics for usage and reservations in ClusterQueues and LocalQueues. (#1206, @trasc)
- Options to allow workloads to borrow quota or preempt other workloads before trying the next flavor in the list (#849, @KunWuLuan)
- Support kubeflow.org/mxjob (#1183, @tenzen-y)
- Support kubeflow.org/paddlejob (#1142, @tenzen-y)
- Support kubeflow.org/pytorchjob (#995, @tenzen-y)
- Support kubeflow.org/tfjob (#1068, @tenzen-y)
- Support kubeflow.org/xgboostjob (#1114, @tenzen-y)
- Workload objects have the label `kueue.x-k8s.io/job-uid` where the value matches the uid of the parent job, whether that's a Job, MPIJob, RayJob, JobSet (#1032, @achernevskii)

### Bug or Regression

- Adjust resources (based on LimitRanges, PodOverhead and resource limits) on existing Workloads when a LocalQueue is created (#1197, @alculquicondor)
- Ensure the ClusterQueue status is updated as the number of pending workloads changes. (#1135, @mimowo)
- Fix resuming of RayJob after preempted. (#1156, @kerthcet)
- Fixed missing create verb for webhook (#1035, @stuton)
- Fixed scheduler to only allow one admission or preemption per cycle within a cohort that has ClusterQueues borrowing quota (#1023, @alculquicondor)
- Helm: Enable the JobSet integration by default (#1184, @tenzen-y)
- Improve job controller to be resilient to API failures during preemption (#1005, @alculquicondor)
- Prevent workloads in ClusterQueue with StrictFIFO from blocking higher priority workloads in other ClusterQueues in the same cohort that require preemption (#1024, @alculquicondor)
- Terminate Kueue when there is an internal failure during setup, so that it can be retried. (#1077, @alculquicondor)

### Other (Cleanup or Flake)

- Add client-go library for AdmissionCheck (#1104, @tenzen-y)
- Add mergeStrategy:merge to all conditions of API objects (#1089, @alculquicondor)
- Update ray-operator to v0.6.0 (#1231, @lowang-bh)