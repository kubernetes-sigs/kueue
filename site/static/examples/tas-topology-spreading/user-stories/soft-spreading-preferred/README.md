# Story 4: soft spreading with limited capacity

From [KEP-13746](../../../../../../keps/13746-tas-topology-spreading/README.md),
"Soft spreading with limited capacity".

The operator prefers zone spreading but accepts that capacity may prevent it, so
the rule uses `enforcementMode: Preferred`: zones over **45%** are deprioritized
in the placement ordering but stay eligible.

## Fixes applied vs. the KEP story

1. **Added `workloadLabelSelectors`.** The story omits it and relies on the KEP's
   mutating-webhook `kueue.x-k8s.io/job-uid` default; the implementation has no
   such injection and rejects a missing selector. For a Deployment that default
   would have been useless regardless — every Pod is its own Workload with its
   own UID, so it would only ever match the Workload being placed. A shared label
   is the only thing that groups the replicas, which is what the KEP's own
   Story 1 says.
2. **Added `kueue.x-k8s.io/podset-required-topology`**, as in
   [../small-model-deployment/](../small-model-deployment/) — the spreading
   annotation is rejected without it.

The KEP's service name is kept: each story here has its own namespace, so
Story 1 and Story 4 no longer collide (they would otherwise share a name *and*
merge into one spreading group with contradictory enforcement modes).

## Why two zones

The cluster deliberately has one zone fewer than the rule wants. `"0.45"` opens
`ceil(1/0.45) = 3` domains before any is reused, but only two exist. After one
replica per zone, both sit at 50% > 45%, so from the third replica on **every**
zone is over the threshold and lands in the lowest scoring tier (`spreadTier`
returns 2, `pkg/cache/scheduler/tas_flavor_snapshot.go:425`).

* With `Preferred`, admission continues, least-loaded zone first
  (`compareSpreadPriority`), and no condition is set.
* With `Required`, `filterBannedDomains` would empty the candidate list and the
  workload would wait — which is the difference this example exists to show.

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
kubectl apply -f deployment.yaml
```

## Verify

```sh
# All six admitted, none pending.
kubectl -n story-soft-spreading get workloads

# No spreading-related condition on the crowded-zone replicas.
kubectl -n story-soft-spreading get workloads \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{range .status.conditions[*]}{.type}={.status}{" "}{end}{"\n"}{end}'

# The zone each replica landed in.
kubectl -n story-soft-spreading get pods -o custom-columns=\
POD:.metadata.name,NODE:.spec.nodeName --no-headers |
  while read -r pod node; do
    echo "$pod $(kubectl get node "$node" -o jsonpath='{.metadata.labels.topology\.kubernetes\.io/zone}')"
  done | sort -k2
```

## Measured behaviour

Verified on kind with two zones and six replicas: **three replicas per zone**,
all admitted, none pending, and no spreading-related condition on any of them -
which is correct `Preferred` semantics, since from the third replica on every
zone is over its 45% allowance and a crowded-domain placement is exactly what
the mode permits.

The alternation is what the rule produces, step by step (`N` is the replicas
placed so far, a zone is over its allowance when `count > 0.45 * N`):

| Replica | `N` | allowance | zone-a | zone-b | placed in |
|---|---|---|---|---|---|
| 1 | 0 | cold start | 0 | 0 | zone-a |
| 2 | 1 | 0.45 | 1 over | 0 under | zone-b |
| 3 | 2 | 0.9 | 1 over | 1 over | zone-a (tied, least loaded) |
| 4 | 3 | 1.35 | 2 over | 1 under | zone-b |
| 5 | 4 | 1.8 | 2 over | 2 over | zone-a (tied) |
| 6 | 5 | 2.25 | 3 over | 2 under | zone-b |

Contrast, same cluster and workload with `enforcementMode: "Required"`:

```
replica 1 -> zone-a          Admitted=True
replica 2 -> zone-b          Admitted=True
replica 3 -> (SchedulingGated) no admission
    event:   TopologyPlacementFailed
    message: couldn't assign flavors to pod set main: topology spreading
             excludes all topology domains at level: topology.kubernetes.io/zone
```

That is the intended behaviour for a capacity-constrained `Required` rule, and
it is the contrast this example exists to draw: the same two-zone cluster that
leaves `Required` with nowhere to put the third replica lets `Preferred` place
all six, as evenly as the zones allow.

### Note for anyone reading older notes on this branch

Earlier revisions of this folder recorded that `Preferred` never spread - every
replica landing in one zone. Two defects were behind it, both now fixed:
spreading counted nothing at all on a Topology whose lowest level is not
`kubernetes.io/hostname` (see [../README.md](../README.md)), and, once counting
worked, the spread-aware ordering was discarded by the best-fit domain re-pick,
which prefers the *most* loaded domain because it is the tighter fit.
`findLevelWithFitDomains` now narrows the candidates to the domains the rules
rank equally best before optimizing capacity, so best-fit chooses within a
spreading tier instead of across tiers. `Required` was never affected, because
banning is a hard filter applied before best-fit runs.
