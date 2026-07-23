#!/usr/bin/env bash

# Copyright 2026 The Kubernetes Authors.
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

set -euo pipefail

TMPDIR=$(mktemp -d)
git clone --depth 1 https://github.com/lindenb/makefile2graph.git "$TMPDIR/makefile2graph" >/dev/null
make -C "$TMPDIR/makefile2graph" >/dev/null
LC_ALL=C make -Bnd --no-print-directory verify | "$TMPDIR/makefile2graph/make2graph" > hack/verify.makefile2graph.dot
if command -v dot >/dev/null 2>&1
  then dot -Tsvg hack/verify.makefile2graph.dot -o hack/verify.makefile2graph.svg; fi
echo "OK"