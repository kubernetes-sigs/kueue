# Release process

To begin the process, open an [issue](https://github.com/kubernetes-sigs/kueue/issues/new/choose)
using the **New Release** template.

## Release cycle

- Kueue aims for one minor release every two months, which allows to align with
  the Kubernetes release cycle every second minor release of Kueue.
- The "aligned" minor release of Kueue is around a month after the release of
  Kubernetes to compile Kueue against that release when the first patch
  release of Kubernetes is already available.
- The release cadence is not rigid and we allow a release may slip, for example,
  due to waiting for an important feature or bug-fixes. However, we strive it
  does not slip more than two weeks.
- When a release slips it does not impact the target release date for the next
  minor release.

## Patch releases

When working on the next N-th minor version of Kueue we continue to maintain
N-1 and N-2 releases. The release branches corresponding to the next patch
releases are regularly tested by CI. Patch releases are released on as-needed
basis.

We follow the Kubernetes cherry-pick [principles](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-release/cherry-picks.md#what-kind-of-prs-are-good-for-cherry-picks), but the choice of cherry-picks
is more relaxed, eg. we allow to cherry-pick minor improvements for [alpha Kueue features](https://kueue.sigs.k8s.io/docs/installation/#change-the-feature-gates-configuration).
