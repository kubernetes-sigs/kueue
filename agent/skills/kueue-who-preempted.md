# Skill: Who Preempted My Workload

Use this when a user asks why their workload was evicted or preempted, or wants to identify what preempted it.

## Context

When kueue preempts a workload it writes a `Normal/Preempted` Kubernetes event onto the victim workload. This event is written unconditionally — it does not depend on log verbosity. The event message embeds both the preemptor's workload UID and parent job UID, plus priority info:

```
Preempted to accommodate a workload (UID: <preemptor-workload-uid>, JobUID: <job-uid>) due to <reason>; preemptor path: <cohort/cq>; preemptee path: <cohort/cq>; preemptor effective priority: <n> (base: <n>, boost: <n>); preemptee effective priority: <n> (base: <n>, boost: <n>)
```

The same message is also written to the workload's `Evicted` status condition **without** the priority suffix, and persists longer than events (events expire after ~1 hour by default):

```
Preempted to accommodate a workload (UID: <preemptor-workload-uid>, JobUID: <job-uid>) due to <reason>; preemptor path: <cohort/cq>; preemptee path: <cohort/cq>
```

The kueue controller manager also logs these events at DEBUG keyed by the victim workload's UID in the structured JSON fields. This is the last-resort source if both the event and the `Evicted` condition have been lost.

### Multi-namespace ClusterQueues

A ClusterQueue can be accessed by LocalQueues in **different namespaces**. This means the preemptor workload may live in a namespace entirely separate from the victim's. Whether you can look up that workload depends on your RBAC access:

| Scenario | Who can resolve the preemptor |
|---|---|
| You have access to all namespaces (cluster admin / `list workloads --all-namespaces`) | You — follow **Path A** in Step 4 |
| You only have access to one or a few namespaces | You may not — follow **Path B** in Step 4 and escalate to a cluster admin if needed |

## Steps

### 1. Get the victim workload name and namespace from the user

If they only give a workload name, search all namespaces.

### 2. Find the Preempted event

```bash
# With namespace
kubectl get events -n <namespace> \
  --field-selector involvedObject.name=<workload-name>,reason=Preempted \
  -o jsonpath='{.items[0].message}'

# All namespaces
kubectl get events -A \
  --field-selector involvedObject.name=<workload-name>,reason=Preempted \
  -o jsonpath='{.items[0].message}'
```

If no event is found (expired), fall back to the status condition:

```bash
kubectl get kueueworkload <name> -n <namespace> \
  -o jsonpath='{.status.conditions[?(@.type=="Evicted")].message}'
```

### 3. Parse the message

Extract:
- `UID` — preemptor workload UID
- `JobUID` — preemptor parent job UID
- `reason` — human-readable preemption reason
- `preemptor path` — ClusterQueue path of the preemptor (e.g. `/cohort/cq-name`)
- `preemptee path` — ClusterQueue path of the victim

### 4. Find the preemptor workload

Since LocalQueues in different namespaces can feed the same ClusterQueue, the preemptor workload may live in a namespace different from the victim's.

`kubectl get kueueworkload -A` requires cluster-scoped `list` permission — it does **not** gracefully show only accessible namespaces; it returns `403 Forbidden` if you lack that permission. Use the access check below to pick the right path:

```bash
kubectl auth can-i list kueueworkloads --all-namespaces
```

#### Path A: All-namespace access (admin)

If `JobUID` is not `UNKNOWN`, use the `kueue.x-k8s.io/job-uid` label selector — it is indexed and the most reliable lookup:

```bash
kubectl get kueueworkload -A -l kueue.x-k8s.io/job-uid=<job-uid> -o json
```

If `JobUID` is `UNKNOWN` (prebuilt or manually created workload with no parent job), fall back to matching on the workload UID from the event directly:

```bash
kubectl get kueueworkload -A -o json | \
  jq '.items[] | select(.metadata.uid == "<preemptor-workload-uid>")'
```

Proceed to Step 5 with the returned workload.

#### Path B: Limited namespace access

`-A` will be forbidden. Try to enumerate the namespaces you can see; note that namespace listing may also be forbidden — always include the victim's namespace as a fallback:

```bash
# Attempt to list visible namespaces; this may itself be forbidden
NAMESPACES=$(kubectl get namespaces -o jsonpath='{.items[*].metadata.name}' 2>/dev/null)

# Always include the victim's namespace in case namespace listing failed
NAMESPACES=$(echo "$NAMESPACES <victim-namespace>" | tr ' ' '\n' | sort -u | tr '\n' ' ')

# Search each accessible namespace for the preemptor workload
# If JobUID is UNKNOWN, match on .metadata.uid instead
for ns in $NAMESPACES; do
  kubectl get kueueworkload -n $ns -l kueue.x-k8s.io/job-uid=<job-uid> -o json 2>/dev/null
done
```

If any namespace returns a match, proceed to Step 5 with that workload.

If no namespace returns a match, the preemptor workload lives in a namespace you cannot access. Stop and tell the user:

> The preemptor workload (JobUID: `<job-uid>`) was not found in any namespace accessible to you. It likely lives in a namespace you do not have access to — this is common when multiple teams share a ClusterQueue via LocalQueues in different namespaces. Please contact a cluster admin to run this skill with all-namespace access.

### 5. Report to the user

Show:
- Preemptor workload: `<namespace>/<name>`
- Preemptor parent job name: `.metadata.ownerReferences[0].name`
- Preemptor parent job type: `.metadata.ownerReferences[0].kind` + `.metadata.ownerReferences[0].apiVersion`
- Preemption reason
- Preemptor ClusterQueue path
- Victim ClusterQueue path
