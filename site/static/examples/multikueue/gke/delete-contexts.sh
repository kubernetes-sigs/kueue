#!/bin/bash

delete_all_k8s_contexts() {
  contexts=$(kubectl config get-contexts -o name)

  for context in $contexts; do
    kubectl config delete-context "$context"
  done
}

delete_all_k8s_contexts
