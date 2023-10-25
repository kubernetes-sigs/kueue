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