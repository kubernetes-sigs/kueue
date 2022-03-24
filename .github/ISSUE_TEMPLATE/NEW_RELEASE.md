---
name: New Release
about: Propose a new release
title: Release v0.x.0
assignees: ahg-g, alculquicondor, ArangoGutierrez, denkensk

---

## Release Checklist
<!--
Please do not remove items from the checklist
-->
- [ ] All [OWNERS](https://github.com/kubernetes-sigs/kueue/blob/master/OWNERS) must LGTM the release proposal
- [ ] Verify that the changelog in this issue is up-to-date
- [ ] For major releases (v0.$MAJ.0) create new release branch
  - [ ] an OWNER creates a vanilla release branch with
        `git branch release-0.$MAJ master`
  - [ ] An OWNER pushes the new release branch with
        `git push release-0.$MAJ`
- [ ] Update things like README, deployment templates, docs, configuration, test/e2e flags, submit a PR against the release branch
- An OWNER prepares a draft release
  - [ ] Create a draft release at [Github releases page](https://github.com/kubernetes-sigs/kueue/releases)
  - [ ] Write the change log into the draft release
  - [ ] Upload release artefacts
- [ ] An OWNER runs
     `git tag -s $VERSION`
      and inserts the changelog into the tag description.
- [ ] An OWNER pushes the tag with
      `git push $VERSION`
  - Triggers prow to build and publish a staging container image
      `gcr.io/k8s-staging-kueue/kueue:$VERSION`
- [ ] Submit a PR against [k8s.io](https://github.com/kubernetes/k8s.io), updating `k8s.gcr.io/images/k8s-staging-kueue/images.yaml` to promote the container images (both "full" and "minimal" variants) to production
- [ ] Wait for the PR to be merged and verify that the image (`k8s.gcr.io/kueue/kueue:$VERSION`) is available.
- [ ] Publish the draft release prepared at the [Github releases page](https://github.com/kubernetes-sigs/kueue/releases)
      which will also trigger a Helm repo index update to add the latest release
- [ ] Add a link to the tagged release in this issue.
- [ ] Send an announcement email to `sig-scheduling@kubernetes.io` and `wg-batch@kubernetes.io` with the subject `[ANNOUNCE] kueue $VERSION is released`
- [ ] Add a link to the release announcement in this issue
- [ ] For a major release (or a point release of the latest major release), update README in master branch
  - [ ] Update references on deployment yamls
  - [ ] Wait for the PR to be merged
- [ ] For a major release, create an unannotated *devel* tag in the master branch, on the first commit that gets merged after the release branch has been created (presumably the README update commit above), and, push the tag:
      `DEVEL=v0.$(($MAJ+1)).0-devel; git tag $DEVEL master && git push $DEVEL`
      This ensures that the devel builds on the master branch will have a meaningful version number.
- [ ] Close this issue


## Changelog
<!--
Describe changes since the last release here.
-->
