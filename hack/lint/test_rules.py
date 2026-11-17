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

"""Check the shared lint rules against positive and negative examples."""

import json
from pathlib import Path
import subprocess
import sys

root = Path(__file__).resolve().parents[2]
result = subprocess.run(
    [sys.argv[1], "run", "--enable-only=gocritic",
     "--show-stats=false", "--output.json.path=stdout", "--output.text.path=/dev/null",
     "./hack/lint/testdata"],
    cwd=root, capture_output=True, text=True, check=False,
)
if result.returncode != 1:
    sys.exit(f"Expected lint findings, got {result.returncode}:\n{result.stderr}")
issues = json.loads(result.stdout)["Issues"]
positions = sorted(issue["Pos"]["Line"] for issue in issues)
expected_positions = [
    line_number for line_number, line in enumerate(
        (root / "hack/lint/testdata/context.go").read_text().splitlines(), start=1
    ) if "// want silencedContext" in line
]
if positions != expected_positions or any(
    issue["FromLinter"] != "gocritic" or "ruleguard:" not in issue["Text"]
    for issue in issues
):
    sys.exit(f"Unexpected lint findings: {issues}")
print("Lint rule examples passed (2 discarded contexts, 3 valid cases).")
