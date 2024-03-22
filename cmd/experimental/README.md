## Experimental integrations

In this folder, you will find integrations of resources with Kueue that are not part of the
kueue binary.

The Kueue project is not committed to maintaining these integrations long term. They can be used
as samples you can adapt to support arbitrary resources in Kueue.

Based on user feedback, we might choose to move some of these integrations into the main binary,
where they will be maintained long term.

### Existing integrations

- TBD

### Adding new integrations

If you would like to contribute with an integration, please start by opening an issue in
https://github.com/kubernetes-sigs/kueue/issues

Keep in mind the following rules for each integration:
- It should have it's own `go.mod` and (optionally) `Makefile`.
- The releases of Kueue won't include binaries and/or images for the experimental integrations.
  Users need to build the images from source. Kueue's CI will verify that the images can be build.
- It can use packages in `sigs.k8s.io/kueue`, but it needs to point to a specific version, even if
  it's not released. If you need any changes on these packages to support the experimental
  integration, they should be submitted in a separate PR.
- The folder should contain an [OWNERS file](https://go.k8s.io/owners).
- If an integration breaks, and the OWNERS are unresponsive, the [Kueue maintainers](/OWNERS) will
  mark the integration as stale for at most 2 releases. After that, Kueue maintainers will remove
  the folder.
- Based on user feedback, the [Kueue maintainers](/OWNERS), at their discretion, might choose to
  move the [integration to pkg/controller/jobs](https://kueue.sigs.k8s.io/docs/tasks/dev/integrate_a_custom_job/).
