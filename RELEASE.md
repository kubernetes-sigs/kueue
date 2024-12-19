# Release process

Kueue is released on an as-needed basis.

To begin the process, open an [issue](https://github.com/kubernetes-sigs/kueue/issues/new/choose)
using the **New Release** template.

### Update component_metadata to align with the kueue version for new releases

If the changes include a code rebase from the `kubernetes-sigs/kueue repository`, ensure `config/component_metadata.yaml` file is updated with the respective Kueue version for new releases
