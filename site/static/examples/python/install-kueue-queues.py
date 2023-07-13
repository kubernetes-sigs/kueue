#!/usr/bin/env python3

from kubernetes import utils, config, client
import tempfile
import requests
import argparse

# install-kueue-queues.py will:
# 1. install queue from the latest or a specific version on GitHub
# This example will demonstrate installing Kueue and applying a YAML file (local) to install Kueue

# Make sure your cluster is running!
config.load_kube_config()
crd_api = client.CustomObjectsApi()
api_client = crd_api.api_client


def get_parser():
    parser = argparse.ArgumentParser(
        description="Submit Kueue Job Example",
        formatter_class=argparse.RawTextHelpFormatter,
    )
    parser.add_argument(
        "--version",
        help="Version of Kueue to install (if undefined, will install from master branch)",
        default=None,
    )
    return parser


def main():
    """
    Install Kueue and the Queue components.

    This will error if they are already installed.
    """
    parser = get_parser()
    args, _ = parser.parse_known_args()
    install_kueue(args.version)


def get_install_url(version):
    """
    Get the install version.

    If a version is specified, use it. Otherwise install
    from the main branch.
    """
    if version is not None:
        return f"https://github.com/kubernetes-sigs/kueue/releases/download/v{version}/manifests.yaml"
    return "https://github.com/kubernetes-sigs/kueue/config/default?ref=main"


def install_kueue(version):
    """
    Install Kueue of a particular version.
    """
    print("⭐️ Installing Kueue...")
    url = get_install_url(version)
    with tempfile.NamedTemporaryFile(delete=True) as install_yaml:
        res = requests.get(url)
        assert res.status_code == 200
        install_yaml.write(res.content)
        utils.create_from_yaml(api_client, install_yaml.name)


if __name__ == "__main__":
    main()
