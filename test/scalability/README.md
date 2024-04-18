# Scalability test

Is a test meant to detect regressions in the Kueue's overall scheduling capabilities. 

# Components
In order to achieve this the following components are used:

## Runner

An application able to:
- generate a set of Kueue specific objects based on a config following the schema of [default_generator_config](`./default_generator_config.yaml`)
- mimic the execution of the workloads
- monitor the created object and generate execution statistics based on the received events

Optionally it's able to run an instance of [minimalkueue](#MinimalKueue) in a dedicated [envtest](https://book.kubebuilder.io/reference/envtest.html) environment.

## MinimalKueue

A light version of the Kueue's controller manager consisting only of the core controllers and the scheduler.  

It is designed to offer the Kueue scheduling capabilities without any additional components which may flood the optional cpu profiles taken during it's execution.


## Checker

Checks the results of a scalability against a set of expected value defined as [default_rangespec](./default_rangespec.yaml).

# Usage

## Run in an existing cluster

```bash
make run-scalability-in-cluster
```

Will run a scalability scenario against an existing cluster (connectable by the host's default kubeconfig), and store the resulting artifacts are stored in `$(PROJECT_DIR)/bin/run-scalability-in-cluster`.

The generation config to be used can be set in `SCALABILITY_GENERATOR_CONFIG` by default using `$(PROJECT_DIR)/test/scalability/default_generator_config.yaml`

Check [installation guide](https://kueue.sigs.k8s.io/docs/installation) for cluster and [observability](https://kueue.sigs.k8s.io/docs/installation/#add-metrics-scraping-for-prometheus-operator).

## Run with minimalkueue

```bash
make run-scalability
```

Will run a scalability scenario against an [envtest](https://book.kubebuilder.io/reference/envtest.html) environment
and an instance of minimalkueue.
The resulting artifacts are stored in `$(PROJECT_DIR)/bin/run-scalability`.

The generation config to be used can be set in `SCALABILITY_GENERATOR_CONFIG` by default using `$(PROJECT_DIR)/test/scalability/default_generator_config.yaml`

Setting `SCALABILITY_CPU_PROFILE=1` will generate a cpuprofile of minimalkueue in `$(PROJECT_DIR)/bin/run-scalability/minimalkueue.cpu.prof`

Setting `SCALABILITY_KUEUE_LOGS=1` will save the logs of minimalkueue in  `$(PROJECT_DIR)/bin/run-scalability/minimalkueue.out.log` and  `$(PROJECT_DIR)/bin/run-scalability/minimalkueue.err.log`

## Run scalability test

```bash
make test-scalability
```

Runs the scalability with minimalkueue and checks the results against `$(PROJECT_DIR)/test/scalability/default_rangespec.yaml`
