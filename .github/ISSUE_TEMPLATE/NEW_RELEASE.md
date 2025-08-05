---
name: New Release
about: Propose a new release
title: Release v0.x.0
assignees: mimowo, tenzen-y

---

## Release Checklist
<!--
Please do not remove items from the checklist
-->
- [ ] [OWNERS](https://github.com/kubernetes-sigs/kueue/blob/main/OWNERS) must LGTM the release proposal.
  At least two for minor or major releases. At least one for a patch release.
- [ ] Verify that the changelog in this issue and the CHANGELOG folder is up-to-date
  - [ ] Use https://github.com/kubernetes/release/tree/master/cmd/release-notes to gather notes.
    Example: `release-notes --org kubernetes-sigs --repo kueue --branch release-0.3 --start-sha 4a0ebe7a3c5f2775cdf5fc7d60c23225660f8702 --end-sha a51cf138afe65677f5f5c97f8f8b1bc4887f73d2 --dependencies=false --required-author=""`
- [ ] For major or minor releases (v$MAJ.$MIN.0), create a new release branch.
  - [ ] An OWNER creates a vanilla release branch with
        `git branch release-$MAJ.$MIN main`
  - [ ] An OWNER pushes the new release branch with
        `git push upstream release-$MAJ.$MIN`
- [ ] Update the release branch:
  - [ ] Update `RELEASE_BRANCH` and `RELEASE_VERSION` in `Makefile` and run `make prepare-release-branch`
  - [ ] Update the `CHANGELOG`
  - [ ] Submit a pull request with the changes: <!-- PREPARE_PULL --> <!-- example #211 #214 -->
- [ ] An OWNER creates a signed tag running
     `git tag -s $VERSION`
      and inserts the changelog into the tag description.
      To perform this step, you need [a PGP key registered on github](https://docs.github.com/en/authentication/managing-commit-signature-verification/checking-for-existing-gpg-keys).
- [ ] An OWNER pushes the tag with
      `git push upstream $VERSION`
  - Triggers prow to build and publish a staging container image
      `us-central1-docker.pkg.dev/k8s-staging-images/kueue/kueue:$VERSION`
- [ ] An OWNER [prepares a draft release](https://github.com/kubernetes-sigs/kueue/releases)
  - [ ] Create the draft release poiting out to the created tag.
  - [ ] Write the change log into the draft release.
  - [ ] Run
      `make artifacts IMAGE_REGISTRY=registry.k8s.io/kueue GIT_TAG=$VERSION`
      to generate the artifacts in the `artifacts` folder.
  - [ ] Upload the files in the `artifacts` folder to the draft release - either
      via UI or `gh release --repo kubernetes-sigs/kueue upload $VERSION artifacts/*`.
- [ ] Submit a PR against [k8s.io](https://github.com/kubernetes/k8s.io) to
      [promote the container images and Helm Chart](https://github.com/kubernetes/k8s.io/tree/main/registry.k8s.io#image-promoter)
      to production: <!-- example kubernetes/k8s.io#7899 -->
  - [ ] Update `registry.k8s.io/images/k8s-staging-kueue/images.yaml`.
- [ ] Wait for the PR to be merged and verify that the image `registry.k8s.io/kueue/kueue:$VERSION` is available.
- [ ] Publish the draft release prepared at the [GitHub releases page](https://github.com/kubernetes-sigs/kueue/releases).
      Link: <!-- example https://github.com/kubernetes-sigs/kueue/releases/tag/v0.1.0 -->
- [ ] Run the [openvex action](https://github.com/kubernetes-sigs/kueue/actions/workflows/openvex.yaml) to generate openvex data. The action will add the file to the release artifacts.
- [ ] Run the [SBOM action](https://github.com/kubernetes-sigs/kueue/actions/workflows/sbom.yaml) to generate the SBOM and add it to the release.
- [ ] Update the `main` branch :
  - [ ] Update `RELEASE_VERSION` in `Makefile` and run `make prepare-release-branch`
  - [ ] Release notes in the `CHANGELOG`
  - [ ] `SECURITY-INSIGHTS.yaml` values by running `make update-security-insights GIT_TAG=$VERSION`
  - [ ] Submit a pull request with the changes: <!-- example #3007 -->
  - [ ] Cherry-pick the pull request onto the `website` branch
- [ ] For major or minor releases, merge the `main` branch into the `website` branch to publish the updated documentation.
- [ ] Send an announcement email to `sig-scheduling@kubernetes.io` and `wg-batch@kubernetes.io` with the subject `[ANNOUNCE] kueue $VERSION is released`.   <!--Link: example https://groups.google.com/a/kubernetes.io/g/wg-batch/c/-gZOrSnwDV4 -->
- [ ] For a major or minor release, prepare the repo for the next version:
  - [ ] Create an unannotated _devel_ tag in the
        `main` branch, on the first commit that gets merged after the release
         branch has been created (presumably the README update commit above), and, push the tag:
        `DEVEL=v$MAJ.$(($MIN+1)).0-devel; git tag $DEVEL main && git push upstream $DEVEL`
        This ensures that the devel builds on the `main` branch will have a meaningful version number.
  - [ ] Create a milestone for the next minor release and update prow to set it automatically for new PRs:
        <!-- example https://github.com/kubernetes/test-infra/pull/30222 -->
  - [ ] Create the presubmits and the periodic jobs for the next patch release:
        <!-- example: https://github.com/kubernetes/test-infra/pull/34561 -->
  - [ ] Drop CI Jobs for testing the out-of-support branch:
        <!-- example: https://github.com/kubernetes/test-infra/pull/34562 -->


## Changelog

```markdown
Describe changes since the last release here.
```
