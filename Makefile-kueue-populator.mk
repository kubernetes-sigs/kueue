# Copyright 2025 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

##@ Kueue Populator

.PHONY: kueue-populator-test
kueue-populator-test: ## Run unit tests for kueue-populator.
	$(MAKE) -C cmd/experimental/kueue-populator test

.PHONY: kueue-populator-test-integration
kueue-populator-test-integration: ## Run integration tests for kueue-populator.
	$(MAKE) -C cmd/experimental/kueue-populator test-integration

.PHONY: kueue-populator-test-e2e
kueue-populator-test-e2e: ## Run e2e tests for kueue-populator.
	$(MAKE) -C cmd/experimental/kueue-populator test-e2e

.PHONY: kueue-populator-verify
kueue-populator-verify: ## Run all verification tests for kueue-populator.
	$(MAKE) -C cmd/experimental/kueue-populator verify
