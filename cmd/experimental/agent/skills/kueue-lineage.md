# Skill: Trace Workload Lineage to Pods

Use this when a user wants to trace lineage across any tier: workload → pods, pod → workload, job → workload, etc. The user may provide a resource at **any level** (Workload, Job, Pod, JobSet, etc.) — always produce the full tree from Workload down to Pods.

## Context

Every kueue Workload stores its parent job's identity in `ownerReferences`. The `kueue.x-k8s.io/job-uid` label on the Workload holds the UID of that parent, which enables upward traversal from any resource tier.

| Field | Value |
|---|---|
| `.metadata.ownerReferences[0].apiVersion` | Parent job API version (e.g. `batch/v1`, `jobset.x-k8s.io/v1alpha2`) |
| `.metadata.ownerReferences[0].kind` | Parent job kind (e.g. `Job`, `JobSet`) |
| `.metadata.ownerReferences[0].name` | Parent job name |
| `kueue.x-k8s.io/job-uid` (label) | Parent job UID |

Each job type sets specific labels on its pods, derived from `PodLabelSelector()` in each controller.

## Steps

### Step 0: Find the Workload (skip if user already provided a workload name)

Walk **up** the `ownerReferences` chain from the given resource, checking at each level whether a Workload with a matching `kueue.x-k8s.io/job-uid` label exists. Stop as soon as one is found.

```bash
# 1. Get the UID of the resource you were given
UID=$(kubectl get <kind> <name> -n <ns> -o jsonpath='{.metadata.uid}')

# 2. Check if a Workload owns this resource directly
kubectl get kueueworkloads -n <ns> -l kueue.x-k8s.io/job-uid=$UID

# 3. If not found, follow ownerReferences one level up and repeat:
OWNER_KIND=$(kubectl get <kind> <name> -n <ns> \
  -o jsonpath='{.metadata.ownerReferences[0].kind}')
OWNER_NAME=$(kubectl get <kind> <name> -n <ns> \
  -o jsonpath='{.metadata.ownerReferences[0].name}')
# → repeat step 0 with OWNER_KIND / OWNER_NAME
```

**Special cases:**

- **Pod group pod** (pod has `kueue.x-k8s.io/pod-group-name` label, no ownerRefs):
  The Workload name equals the group name — no UID lookup needed:
  ```bash
  GROUP=$(kubectl get pod <name> -n <ns> \
    -o jsonpath='{.metadata.labels.kueue\.x-k8s\.io/pod-group-name}')
  kubectl get kueueworkload $GROUP -n <ns>
  ```

- **Deployment pod** (pod has `kueue.x-k8s.io/managed=true`, no `pod-group-name`):
  Kueue creates one Workload per pod; the pod's UID is the direct match:
  ```bash
  UID=$(kubectl get pod <name> -n <ns> -o jsonpath='{.metadata.uid}')
  kubectl get kueueworkloads -n <ns> -l kueue.x-k8s.io/job-uid=$UID
  ```

- **StatefulSet pod**: pod ownerRef points directly to the StatefulSet (no ReplicaSet).
  Walk Pod → StatefulSet, then look up by StatefulSet UID.

Once a Workload is found, proceed to Step 1.

---

### Step 1: Get the workload's parent job

```bash
kubectl get kueueworkload <name> -n <namespace> \
  -o jsonpath='apiVersion={.metadata.ownerReferences[0].apiVersion} kind={.metadata.ownerReferences[0].kind} name={.metadata.ownerReferences[0].name} ns={.metadata.namespace}'
```

---

### Step 2: Fetch intermediate resources and pods by job type

Use the `kind` from `ownerReferences` to pick the right path:

#### batch/Job
No intermediate resources.
```bash
kubectl get pods -n <ns> -l batch.kubernetes.io/job-name=<name>
```

#### JobSet (`jobset.x-k8s.io`)
```bash
# Intermediate: child Jobs
kubectl get jobs -n <ns> -l jobset.sigs.k8s.io/jobset-name=<name>
# Pods (label propagates through Jobs to Pods)
kubectl get pods -n <ns> -l jobset.sigs.k8s.io/jobset-name=<name>
```
Useful additional labels on jobs/pods: `jobset.sigs.k8s.io/replicatedjob-name`, `jobset.sigs.k8s.io/job-index`.

#### TrainJob (`trainer.kubeflow.org`)
TrainJob creates a JobSet internally; pods carry the JobSet label with the TrainJob name:
```bash
kubectl get pods -n <ns> -l jobset.sigs.k8s.io/jobset-name=<name>
```

#### RayJob (`ray.io`)
RayJob creates a RayCluster dynamically. Get the cluster name from status first:
```bash
CLUSTER=$(kubectl get rayjob <name> -n <ns> -o jsonpath='{.status.rayClusterName}')
kubectl get pods -n <ns> -l ray.io/cluster=$CLUSTER
```

#### RayCluster (`ray.io`)
```bash
kubectl get pods -n <ns> -l ray.io/cluster=<name>
```

#### RayService (`ray.io`)
The cluster name is in `activeServiceStatus` once healthy, but falls back to `pendingServiceStatus` during startup or upgrade:
```bash
CLUSTER=$(kubectl get rayservice <name> -n <ns> \
  -o jsonpath='{.status.activeServiceStatus.rayClusterName}')
# If empty (service still initializing or upgrading), use pendingServiceStatus
CLUSTER=${CLUSTER:-$(kubectl get rayservice <name> -n <ns> \
  -o jsonpath='{.status.pendingServiceStatus.rayClusterName}')}
kubectl get pods -n <ns> -l ray.io/cluster=$CLUSTER
```

#### LeaderWorkerSet (`leaderworkerset.sigs.k8s.io`)
```bash
kubectl get pods -n <ns> -l leaderworkerset.sigs.k8s.io/name=<name>
```
Additional pod labels: `leaderworkerset.sigs.k8s.io/group-index`, `leaderworkerset.sigs.k8s.io/worker-index`.

#### PyTorchJob (`kubeflow.org`)
```bash
kubectl get pods -n <ns> -l training.kubeflow.org/job-name=<name>,training.kubeflow.org/operator-name=pytorchjob-controller
```

#### TFJob (`kubeflow.org`)
```bash
kubectl get pods -n <ns> -l training.kubeflow.org/job-name=<name>,training.kubeflow.org/operator-name=tfjob-controller
```

#### MPIJob (`kubeflow.org`)
```bash
kubectl get pods -n <ns> -l training.kubeflow.org/job-name=<name>,training.kubeflow.org/operator-name=mpi-operator
```

#### PaddleJob (`kubeflow.org`)
```bash
kubectl get pods -n <ns> -l training.kubeflow.org/job-name=<name>,training.kubeflow.org/operator-name=paddlejob-controller
```

#### JAXJob (`kubeflow.org`)
```bash
kubectl get pods -n <ns> -l training.kubeflow.org/job-name=<name>,training.kubeflow.org/operator-name=jaxjob-controller
```

#### XGBoostJob (`kubeflow.org`)
```bash
kubectl get pods -n <ns> -l training.kubeflow.org/job-name=<name>,training.kubeflow.org/operator-name=xgboostjob-controller
```

#### SparkApplication (`sparkoperator.k8s.io`)
```bash
kubectl get pods -n <ns> -l sparkoperator.k8s.io/app-name=<name>
```

#### AppWrapper (`workload.codeflare.dev`)
```bash
kubectl get pods -n <ns> -l workload.codeflare.dev/appwrapper=<name>
```

#### Pod group (plain pods managed by kueue)
For pod groups, `ownerReferences[0]` points to the lead pod (`kind=Pod`), not a group object. Get the group name from that pod's label, then list all group members:
```bash
# ownerRef kind=Pod → pod group; get group name from the lead pod's label
GROUP=$(kubectl get pod <lead-pod-name> -n <ns> \
  -o jsonpath='{.metadata.labels.kueue\.x-k8s\.io/pod-group-name}')
kubectl get pods -n <ns> -l kueue.x-k8s.io/pod-group-name=$GROUP
```

#### Deployment / StatefulSet (`apps`)
Kueue creates one workload per pod (not per Deployment/StatefulSet). The `ownerReference` on each workload points to an individual `Pod`. Distinguish from pod groups by the absence of `kueue.x-k8s.io/pod-group-name` on the pod.

Trace the ownerReference chain up to the Deployment/StatefulSet, then use its selector to find all pods:
```bash
# ownerRef kind=Pod, no pod-group-name label → Deployment or StatefulSet
# Trace: Pod → ReplicaSet → Deployment (or Pod → StatefulSet directly)
RS=$(kubectl get pod <pod-name> -n <ns> \
  -o jsonpath='{.metadata.ownerReferences[0].name}')
DEPLOY=$(kubectl get replicaset $RS -n <ns> \
  -o jsonpath='{.metadata.ownerReferences[0].name}')
# Get selector and find all pods
SELECTOR=$(kubectl get deployment $DEPLOY -n <ns> \
  -o jsonpath='{.spec.selector.matchLabels}')
# e.g. if selector is {"app":"foo"}, run:
kubectl get pods -n <ns> -l app=<value>

# For StatefulSet, ownerRef on Pod points directly to the StatefulSet (no ReplicaSet):
DEPLOY=$(kubectl get pod <pod-name> -n <ns> \
  -o jsonpath='{.metadata.ownerReferences[0].name}')
SELECTOR=$(kubectl get statefulset $DEPLOY -n <ns> \
  -o jsonpath='{.spec.selector.matchLabels}')
```

---

### Step 3: Display the lineage tree

Format output as a tree from Workload down to Pods. If the user started from a pod or intermediate resource, annotate that entry with `← you are here`.

```
Workload:  <namespace>/<workload-name>
└── JobSet: <namespace>/<jobset-name>
    ├── Jobs (3):
    │   ├── <job-name-0>
    │   ├── <job-name-1>           ← you are here  (if user gave a job name)
    │   └── <job-name-2>
    └── Pods (3):
        ├── <pod-name-0> — <node> [Running]
        ├── <pod-name-1> — <node> [Running]  ← you are here  (if user gave a pod name)
        └── <pod-name-2> — <node> [Running]
```
