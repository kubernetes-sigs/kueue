# kueue-priority-booster

Helm chart for deploying the experimental priority-boost controller.

## Install

```bash
helm install kueue-priority-booster ./charts/kueue-priority-booster \
  --namespace kueue-system \
  --create-namespace
```

## Values

Main values are under `kueuePriorityBooster`:

- `image.repository`, `image.tag`, `image.pullPolicy`, `imagePullSecrets`
- `config.timeSharing.duration` — after a workload is **admitted**, the controller waits this long before
  touching `kueue.x-k8s.io/priority-boost`. During the wait it **requeues** with `RequeueAfter`
  (roughly the remaining time); it does **not** rewrite the annotation on a timer. Once the window
  has passed while the workload stays admitted, the annotation is set **once** to `-timeSharing.negativeBoost` and
  left stable (no periodic updates) until admission changes, the workload falls out of selector /
  max priority, or the controller clears it.
- `config.timeSharing.negativeBoost` — unsigned magnitude; after the window Kueue sees annotation `-timeSharing.negativeBoost`.
  Effective priority is `spec.priority` plus the annotation integer
  (`pkg/util/priority.ParseEffectivePriority`). Example: `spec.priority` = `1000`, `timeSharing.negativeBoost` =
  `100000` → annotation `-100000` → effective priority `1000 + (-100000) = -99000`, which makes the
  running workload easier to preempt when within-cluster preemption uses lower effective priority.
  The annotation is negative so the workload becomes *less* urgent; the Helm key stays positive as
  the magnitude used to lower effective priority.
- `config.workloadSelector` — optional `LabelSelector`; workloads that do not match are not
  managed (annotation cleared)
- `config.maxWorkloadPriority` — optional; workloads with higher `spec.priority` are excluded (omit key for no limit)
- `kueue.enabled` — optional bundled Kueue subchart
