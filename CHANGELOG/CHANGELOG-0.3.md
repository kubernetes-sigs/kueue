## v0.3.0

Changes since `v0.2.1`:

### Features

- Upgrade the `config.kueue.x-k8s.io` API version from `v1alpha1` to `v1alpha2`. `v1alpha1` is no longer supported.
  `v1alpha2` includes the following changes:
  - Add Namespace to propagate the namespace where kueue is deployed to the webhook certificate.
  - Add InternalCertManagement with fields Enable, WebhookServiceName and WebhookSecretName.
  - Remove EnableInternalCertManagement. Use InternalCertManagement.Enable instead.

### Bug fixes

- Fix job controller ClusterRole for clusters that enable OwnerReferencesPermissionEnforcement admission control validation
