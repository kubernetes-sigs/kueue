# Story 3: large AI inference model using plain Pod groups

From [KEP-13746](../../../../../../keps/13746-tas-topology-spreading/README.md),
"Serving a large AI inference model with multiple replicas using PodGroups".

An in-house Pod management system integrates with Kueue via Pod groups; one group
is one model replica. A rack may already hold at most **34%** of the groups for
the next one to be placed there, so each of the three groups opens its own rack.

## Fixes applied vs. the KEP story

1. **Removed `kueue.x-k8s.io/podset-group-name`.** This is the important one and
   the reason the story silently does nothing as written — see below.
2. **Added the `kueue.x-k8s.io/queue-name` label**, which the story omits.

The story's explicit `workloadLabelSelectors` on `app` is kept as written: it is
required, and a Pod group has no parent object whose `job-uid` could group the
replicas — which is exactly why the KEP itself says the selector must be given
here.

## Why the group name breaks this story

`GroupKeyForPodSet` (`pkg/util/tas/tas.go:91`) derives the spreading key from
`podset-group-name` when present, otherwise from the PodSet's own name. Counting
then only considers Workloads whose key equals the candidate's:
`occupiedDomainsForGroup` skips every PodSet with a different key
(`pkg/cache/scheduler/tas_spread_tree_count.go:127`).

The story sets `podset-group-name` to the per-replica group
(`large-inference-service-0`, `-1`, …). So replica 1's key is
`podsetgroup/large-inference-service-1`, replica 0's is
`…-0`, and neither ever sees the other. `Total` stays `0`, the cold-start branch
of `ExceedsShare` makes every domain eligible, and all replicas land in one rack.
**No error, no condition, no log** — the annotation looks honoured and is not.

A uniform Pod group needs no group name at all: `constructGroupPodSets` collapses
the group into a single PodSet (`pkg/controller/jobs/pod/pod_controller.go:830`),
so the fallback key `podset/<name>` already identifies "one replica".

Keep the Pods' `kueue.x-k8s.io/pod-group-name` **label** per-replica — a
different annotation, and the one that makes each replica its own Workload.

## Why every Pod spec here is identical

For a Pod group the PodSet name is the Pod's role hash
(`pod_controller.go:857`), so the fallback spreading key is
`podset/<role-hash>`. `GenerateRoleHash` hashes a *shape*, not the whole spec
(`pkg/util/pod/pod.go:133-165`):

* per-container `resources.requests` and `ports`
* `nodeSelector`, `affinity`, `tolerations`
* `runtimeClassName`, `priority`, `topologySpreadConstraints`, `overhead`,
  `resourceClaims`, and pod-level `resources` when set

Container names, images and args are **not** in it. So the replicas need the same
*shape*, not byte-identical specs — but a different CPU request or an extra
toleration on one replica changes its hash, changes its key, and splits it into
its own spreading group. Same silent failure as the group name, triggered by a
one-character edit.

## Run it

```sh
kind create cluster --config kind-cluster.yaml

# Merge featureGates + labelKeysToCopy into Kueue's config and roll it.
kubectl -n kueue-system get cm kueue-manager-config -o jsonpath='{.data.controller_manager_config\.yaml}' > /tmp/kueue-config.yaml
# ... merge kueue-config-patch.yaml into /tmp/kueue-config.yaml ...
kubectl -n kueue-system create cm kueue-manager-config \
  --from-file=controller_manager_config.yaml=/tmp/kueue-config.yaml \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl -n kueue-system rollout restart deployment/kueue-controller-manager

kubectl apply -f sample-queues.yaml
kubectl apply -f podgroups.yaml
```

## Verify

```sh
# Three Workloads, one per Pod group.
kubectl -n story-large-podgroup get workloads

# All PodSet names should be the SAME role hash across the three Workloads -
# that is what makes them one spreading group.
kubectl -n story-large-podgroup get workloads \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.podSets[*].name}{"\n"}{end}'

# The rack each group landed in - both Pods of a group must share it.
kubectl -n story-large-podgroup get pods -o custom-columns=\
POD:.metadata.name,NODE:.spec.nodeName --no-headers |
  while read -r pod node; do
    echo "$pod $(kubectl get node "$node" -o jsonpath='{.metadata.labels.cloud\.provider\.com/rack}')"
  done | sort -k2
```

Expected: three groups in three distinct racks, both Pods of a group in the same
rack. To see the bug the story would have hit, add
`kueue.x-k8s.io/podset-group-name: large-inference-service-<n>` back to each
Pod: all three groups then collapse into one rack with no complaint.

## Notes

* The KEP story uses 4-Pod groups. Shrunk to 2 to keep the manifest readable;
  set `pod-group-total-count` and the number of Pods together if you change it.
* The `pod` integration is enabled by default, and these Pods opt in explicitly
  via `kueue.x-k8s.io/queue-name`, so no `managedJobsNamespaceSelector` change
  is needed.
