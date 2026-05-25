#!/usr/bin/env bash
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

# Retry a command on failure. Captures the command's stdout on success and
# echoes it; stderr passes through. Designed to be invoked from Make $(shell ...)
# where the captured stdout becomes the variable value.
#
# Usage:
#   retry.sh [--attempts N] [--delay SECONDS] [--retry-condition "CMD"] [--cleanup "CMD"] -- <command> [args...]
#
# Defaults: --attempts 4, --delay 5
# Exit:     0 on first success, 1 if all attempts fail, 2 on usage error.

set -u

attempts=4
delay=5
retry_condition=""
cleanup=""

while [ $# -gt 0 ]; do
    case $1 in
        --attempts)        attempts=$2; shift 2 ;;
        --delay)           delay=$2;    shift 2 ;;
        --retry-condition) retry_condition=$2; shift 2 ;;
        --cleanup)         cleanup=$2; shift 2 ;;
        --)                shift; break ;;
        -*)                echo "retry: unknown flag: $1" 1>&2; exit 2 ;;
        *)                 break ;;
    esac
done

if [ $# -eq 0 ]; then
    echo "retry: no command given" 1>&2
    exit 2
fi

for i in $(seq 1 "$attempts"); do
    echo "retry [$i/$attempts]: $*" 1>&2
    if out=$("$@"); then
        printf '%s' "$out"
        exit 0
    fi
    echo "retry [$i/$attempts] failed" 1>&2

    if [ "$i" -lt "$attempts" ]; then
        if [ -n "$retry_condition" ]; then
            # Evaluate the fail-fast condition
            if ! eval "$retry_condition"; then
                echo "retry: aborting early as retry condition failed (fail-fast)" 1>&2
                exit 1
            fi
        fi

        if [ -n "$cleanup" ]; then
            # Run the cleanup command before the next attempt
            echo "retry: running cleanup command..." 1>&2
            eval "$cleanup"
        fi

        sleep "$delay"
    fi
done
exit 1
