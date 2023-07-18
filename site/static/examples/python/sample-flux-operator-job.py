#!/usr/bin/env python3

import argparse
from kubernetes import config, client
import fluxoperator.models as models

# sample-flux-operator.py
# This example will demonstrate full steps to submit a Job via the Flux Operator.

# Make sure your cluster is running!
config.load_kube_config()
crd_api = client.CustomObjectsApi()
api_client = crd_api.api_client


def get_parser():
    parser = argparse.ArgumentParser(
        description="Submit Kueue Flux Operator Job Example",
        formatter_class=argparse.RawTextHelpFormatter,
    )
    parser.add_argument(
        "--job-name",
        help="generateName field to set for job (job prefix does not work here)",
        default="hello-world",
    )
    parser.add_argument(
        "--image",
        help="container image to use",
        default="ghcr.io/flux-framework/flux-restful-api",
    )
    parser.add_argument(
        "--tasks",
        help="Number of tasks",
        default=1,
        type=int,
    )
    parser.add_argument(
        "--quiet",
        help="Do not show extra flux output (only hello worlds!)",
        action="store_true",
        default=False,
    )

    parser.add_argument(
        "--command",
        help="command to run",
        default="echo",
    )
    parser.add_argument(
        "--args", nargs="+", help="args for container", default=["hello", "world"]
    )
    return parser


def generate_minicluster_crd(job_name, image, command, args, quiet=False, tasks=1):
    """
    Generate a minicluster CRD
    """
    container = models.MiniClusterContainer(
        command=command + " " + " ".join(args),
        resources={
            "limits": {
                "cpu": 1,
                "memory": "2Gi",
            }
        },
    )

    # 4 pods and 4 tasks will echo hello-world x 4
    spec = models.MiniClusterSpec(
        job_labels={"kueue.x-k8s.io/queue-name": "user-queue"},
        containers=[container],
        size=4,
        tasks=tasks,
        logging={"quiet": quiet},
    )

    return models.MiniCluster(
        kind="MiniCluster",
        api_version="flux-framework.org/v1alpha1",
        metadata=client.V1ObjectMeta(
            generate_name=job_name,
            namespace="default",
        ),
        spec=spec,
    )


def main():
    """
    Run an MPI job. This requires the MPI Operator to be installed.
    """
    parser = get_parser()
    args, _ = parser.parse_known_args()

    # Generate a CRD spec
    minicluster = generate_minicluster_crd(
        args.job_name, args.image, args.command, args.args, args.quiet, args.tasks
    )
    crd_api = client.CustomObjectsApi()

    print(f"📦️ Container image selected is {args.image}...")
    print(f"⭐️ Creating sample job with prefix {args.job_name}...")
    crd_api.create_namespaced_custom_object(
        group="flux-framework.org",
        version="v1alpha1",
        namespace="default",
        plural="miniclusters",
        body=minicluster,
    )
    print(
        'Use:\n"kubectl get queue" to see queue assignment\n"kubectl get pods" to see pods'
    )


if __name__ == "__main__":
    main()
