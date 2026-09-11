# Story 2: large AI inference model, many replicas (LeaderWorkerSet)

From [KEP-13746](../../../../../../keps/13746-tas-topology-spreading/README.md),
"Serving a large AI inference model with multiple replicas".

The model needs several closely connected Pods, so one LWS group is one replica.
Each group lands in a single rack, and a rack may already hold at most **45%** of
the groups for the next one to be placed there.

## Fix applied vs. the KEP story

* **Added `workloadLabelSelectors`.** The KEP has the Workload mutating webhook
  inject `[{"key": "kueue.x-k8s.io/job-uid", ...}]` when the field is omitted, so
  the story writes no selector at all. The implementation has no such injection
  and rejects a missing or empty selector outright
  (`ErrTopologySpreadingSelectorMissing`), so the story as written is refused at
  LWS admission. Every group's Workload carries the same LWS labels, so any
  shared label works; `app` is used here.

Everything else is the KEP's, including `cloud.provider.com/rack` — this example
ships its own rack-labelled cluster and Topology.

## Why `podset-group-name` matters here

It fuses leader and worker into one spreading unit, so a replica counts **once**
rather than twice: `occupiedDomainsForGroup` unions the domains of every PodSet
sharing the key and increments `Total` a single time.

It must be the **same value for every replica** — which it is, since it comes
from the shared template. Contrast [../large-model-podgroup/](../large-model-podgroup/),
where the KEP gives each replica its *own* group name and thereby switches
spreading off silently.

## Requirements

The LWS integration must be enabled and the
[LeaderWorkerSet controller](https://github.com/kubernetes-sigs/lws) installed.

## Run it

```sh
kind create cluster --config kind-cluster.yaml
# install the LeaderWorkerSet controller, then Kueue

# Merge featureGates + labelKeysToCopy into Kueue's config and roll it.
kubectl -n kueue-system get cm kueue-manager-config -o jsonpath='{.data.controller_manager_config\.yaml}' > /tmp/kueue-config.yaml
# ... merge kueue-config-patch.yaml into /tmp/kueue-config.yaml ...
kubectl -n kueue-system create cm kueue-manager-config \
  --from-file=controller_manager_config.yaml=/tmp/kueue-config.yaml \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl -n kueue-system rollout restart deployment/kueue-controller-manager

kubectl apply -f sample-queues.yaml
kubectl apply -f leaderworkerset.yaml
```

## Verify

```sh
# One Workload per group, each with two PodSets in one rack.
kubectl -n story-large-lws get workloads

# The rack each group landed in - both Pods of a group must share it.
kubectl -n story-large-lws get pods -o custom-columns=\
POD:.metadata.name,NODE:.spec.nodeName --no-headers |
  while read -r pod node; do
    echo "$pod $(kubectl get node "$node" -o jsonpath='{.metadata.labels.cloud\.provider\.com/rack}')"
  done | sort -k2
```

Expected: leader and worker of each replica share a rack, all three racks are
opened before any rack takes a second group, and the six groups end up roughly
`2/2/2`.

## Notes

* The KEP story uses `replicas: 10` with `size: 4`. Scaled to `6`/`2` so the
  example runs on a three-node kind cluster; the spreading behaviour is the same.
* The rule's `topologyKey` may not name a level *below* the one requested by
  `podset-required-topology` — the group lands in a single domain at the
  requested level, so there is no choice left to make below it. Equal levels, as
  here, are fine.
