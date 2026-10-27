#!/usr/bin/env -S uv run
# /// script
# requires-python = ">=3.9"
# dependencies = [
#     "kubeflow",
# ]
# ///

import argparse
from kubeflow.trainer import TrainerClient, CustomTrainer
from kubeflow.trainer.options import Labels

# sample-trainjob.py
# This example demonstrates submitting a Kubeflow Trainer TrainJob, managed by
# Kueue, using the Kubeflow SDK. It runs a small distributed PyTorch training
# job (Fashion-MNIST) against the built-in "torch-distributed" runtime.


def train_pytorch():
    import os

    import torch
    from torch import nn
    import torch.nn.functional as F
    from torchvision import datasets, transforms
    import torch.distributed as dist
    from torch.utils.data import DataLoader, DistributedSampler

    # Kubeflow Trainer configures the distributed environment automatically.
    device, backend = ("cuda", "nccl") if torch.cuda.is_available() else ("cpu", "gloo")
    dist.init_process_group(backend=backend)

    local_rank = int(os.getenv("LOCAL_RANK", 0))
    print(
        "Distributed Training with WORLD_SIZE: {}, RANK: {}, LOCAL_RANK: {}.".format(
            dist.get_world_size(), dist.get_rank(), local_rank
        )
    )

    class Net(nn.Module):
        def __init__(self):
            super(Net, self).__init__()
            self.conv1 = nn.Conv2d(1, 20, 5, 1)
            self.conv2 = nn.Conv2d(20, 50, 5, 1)
            self.fc1 = nn.Linear(4 * 4 * 50, 500)
            self.fc2 = nn.Linear(500, 10)

        def forward(self, x):
            x = F.relu(self.conv1(x))
            x = F.max_pool2d(x, 2, 2)
            x = F.relu(self.conv2(x))
            x = F.max_pool2d(x, 2, 2)
            x = x.view(-1, 4 * 4 * 50)
            x = F.relu(self.fc1(x))
            x = self.fc2(x)
            return F.log_softmax(x, dim=1)

    device = torch.device(f"{device}:{local_rank}")
    model = nn.parallel.DistributedDataParallel(Net().to(device))
    model.train()
    optimizer = torch.optim.SGD(model.parameters(), lr=0.1, momentum=0.9)

    # Download the dataset on the rank-0 process first, then load it on every rank.
    if local_rank == 0:
        datasets.FashionMNIST(
            "./data", train=True, download=True, transform=transforms.ToTensor()
        )
    dist.barrier()
    dataset = datasets.FashionMNIST(
        "./data", train=True, download=False, transform=transforms.ToTensor()
    )
    train_loader = DataLoader(
        dataset, batch_size=100, sampler=DistributedSampler(dataset)
    )

    for epoch in range(3):
        for batch_idx, (inputs, labels) in enumerate(train_loader):
            inputs, labels = inputs.to(device), labels.to(device)
            loss = F.nll_loss(model(inputs), labels)
            optimizer.zero_grad()
            loss.backward()
            optimizer.step()
            if batch_idx % 10 == 0 and dist.get_rank() == 0:
                print(
                    "Train Epoch: {} [{}/{}]\tLoss: {:.6f}".format(
                        epoch,
                        batch_idx * len(inputs),
                        len(train_loader.dataset),
                        loss.item(),
                    )
                )

    dist.barrier()
    if dist.get_rank() == 0:
        print("Training is finished")
    dist.destroy_process_group()


def get_parser():
    parser = argparse.ArgumentParser(
        description="Submit Kueue Kubeflow Trainer TrainJob Example",
        formatter_class=argparse.RawTextHelpFormatter,
    )
    parser.add_argument(
        "--runtime",
        help="Kubeflow Trainer runtime to use",
        default="torch-distributed",
    )
    parser.add_argument(
        "--queue-name",
        help="Kueue local queue name",
        default="user-queue",
    )
    parser.add_argument(
        "--num-nodes",
        help="Number of PyTorch training nodes",
        type=int,
        default=1,
    )
    return parser


def main():
    """
    Submit a TrainJob via the Kubeflow SDK. This requires Kubeflow Trainer to be
    installed, the selected runtime to exist, and Kueue with TrainJob integration.
    """
    parser = get_parser()
    args, _ = parser.parse_known_args()

    client = TrainerClient()
    print(f"⭐️ Submitting TrainJob to runtime {args.runtime}...")
    job_name = client.train(
        runtime=args.runtime,
        trainer=CustomTrainer(
            func=train_pytorch,
            num_nodes=args.num_nodes,
            resources_per_node={"cpu": 1, "memory": "2Gi"},
        ),
        options=[Labels({"kueue.x-k8s.io/queue-name": args.queue_name})],
    )
    print(f"⭐️ Created TrainJob {job_name}")
    print(
        'Use:\n"kubectl get queue" to see queue assignment\n'
        '"kubectl get trainjobs" to see TrainJobs'
    )


if __name__ == "__main__":
    main()
