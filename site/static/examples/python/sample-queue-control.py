#!/usr/bin/env python3

import argparse
from kubernetes import config, client

# sample-queue-control.py
# This will show how to interact with queues

# Make sure your cluster is running!
config.load_kube_config()
crd_api = client.CustomObjectsApi()
api_client = crd_api.api_client


def get_parser():
    parser = argparse.ArgumentParser(
        description="Interact with Queues e",
        formatter_class=argparse.RawTextHelpFormatter,
    )
    parser.add_argument(
        "--namespace",
        help="namespace to list for",
        default="default",
    )
    return parser


def main():
    """
    Get a listing of jobs in the queue
    """
    parser = get_parser()
    args, _ = parser.parse_known_args()

    listing = crd_api.list_namespaced_custom_object(
        group="kueue.x-k8s.io",
        version="v1beta1",
        namespace=args.namespace,
        plural="localqueues",
    )
    list_queues(listing)

    listing = crd_api.list_namespaced_custom_object(
        group="batch",
        version="v1",
        namespace=args.namespace,
        plural="jobs",
    )
    list_jobs(listing)


def list_jobs(listing):
    """
    Iterate and show job metadata.
    """
    if not listing:
        print("💼️ There are no jobs.")
        return

    print("\n💼️ Jobs")
    for job in listing["items"]:
        jobname = job["metadata"]["name"]
        status = (
            "TBA" if "succeeded" not in job["status"] else job["status"]["succeeded"]
        )
        ready = job["status"]["ready"]
        print(f"Found job {jobname}")
        print(f"  Succeeded: {status}")
        print(f"  Ready: {ready}")


def list_queues(listing):
    """
    Helper function to iterate over and list queues.
    """
    if not listing:
        print("⛑️  There are no queues.")
        return

    print("\n⛑️  Local Queues")

    # This is listing queues
    for q in listing["items"]:
        print(f'Found queue {q["metadata"]["name"]}')
        print(f"  Admitted workloads: {q['status']['admittedWorkloads']}")
        print(f"  Pending workloads: {q['status']['pendingWorkloads']}")

        # And flavors with resources
        for f in q["status"]["flavorUsage"]:
            print(f'  Flavor {f["name"]} has resources {f["resources"]}')


if __name__ == "__main__":
    main()
