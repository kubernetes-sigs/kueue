#!/usr/bin/env python3
# Copyright the Kubernetes Authors.
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

"""Print the version a go.mod requires, without downloading module metadata."""

import json
import os
import subprocess
import sys


def module_version(module_dir, module_path, go_cmd="go"):
    """Return the required version of module_path from module_dir/go.mod."""
    result = subprocess.run(
        [go_cmd, "mod", "edit", "-json"],
        cwd=module_dir,
        check=False,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    message = f"failed to resolve {module_path} from {module_dir}/go.mod"
    if result.returncode != 0:
        detail = result.stderr.strip() or result.stdout.strip()
        if detail:
            message = f"{message}: {detail}"
        raise ValueError(message)
    try:
        data = json.loads(result.stdout)
    except json.JSONDecodeError as error:
        raise ValueError(f"{message}: {error}") from error
    requires = data.get("Require") if isinstance(data, dict) else None
    for req in requires or []:
        if isinstance(req, dict) and req.get("Path") == module_path and req.get("Version"):
            return req["Version"]
    raise ValueError(message)


def main(argv):
    if len(argv) != 3:
        raise ValueError("usage: module_version.py <module-dir> <module-path>")
    print(module_version(argv[1], argv[2], os.environ.get("GO_CMD", "go")))


if __name__ == "__main__":
    try:
        main(sys.argv)
    except ValueError as error:
        print(error, file=sys.stderr)
        sys.exit(1)
