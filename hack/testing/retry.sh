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
# By default stdout is captured and echoed only on success, so failed-attempt
# output never pollutes a $(shell ...) caller that captures the result. Use
# --stream when you instead want the command's stdout/stderr forwarded live
# (e.g. for installs whose progress should be visible); --stream forwards output
# from every attempt and returns nothing to capture, so do not combine it with a
# $(shell ...) consumer.
#
# Advanced features:
#   --exponential:       Double the delay after each failed attempt.
#   --stream:            Forward stdout/stderr live instead of capturing stdout.
#   --continue-if "CMD": Evaluates CMD upon failure. If it fails, retries are aborted immediately (fail-fast).
#   --cleanup "CMD": Evaluates CMD before starting the next retry attempt.
#
# Usage:
#   retry.sh [--attempts N] [--delay SECONDS] [--exponential] [--stream] [--continue-if "CMD"] [--cleanup "CMD"] -- <command> [args...]
#
# Defaults: --attempts 4, --delay 5 (no exponential backoff)
# Exit:     0 on first success, 1 if all attempts fail, 2 on usage error.

set -u

attempts=4
delay=5
exponential=false
stream=false
continue_if=""
cleanup=""

while [ $# -gt 0 ]; do
    case $1 in
        --attempts)        attempts=$2; shift 2 ;;
        --delay)           delay=$2;    shift 2 ;;
        --exponential)     exponential=true; shift ;;
        --stream)          stream=true; shift ;;
        --continue-if)     continue_if=$2; shift 2 ;;
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
    if [ "$stream" = "true" ]; then
        # Forward stdout/stderr live; nothing is captured or echoed.
        if "$@"; then
            exit 0
        fi
    elif out=$("$@"); then
        # Capture stdout and echo it only on success, so failed-attempt output
        # never pollutes a $(shell ...) caller that captures the result.
        printf '%s' "$out"
        exit 0
    fi
    if [ "$i" -lt "$attempts" ]; then
        echo "retry [$i/$attempts] failed, retrying in ${delay}s..." 1>&2
    else
        echo "retry [$i/$attempts] failed" 1>&2
    fi

    if [ "$i" -lt "$attempts" ]; then
        if [ -n "$continue_if" ]; then
            # Evaluate the fail-fast condition
            if ! eval "$continue_if"; then
                echo "retry: aborting early as continue-if condition failed (fail-fast)" 1>&2
                exit 1
            fi
        fi

        if [ -n "$cleanup" ]; then
            # Run the cleanup command before the next attempt
            echo "retry: running cleanup command..." 1>&2
            if ! eval "$cleanup"; then
                echo "retry: cleanup command failed, aborting retries" 1>&2
                exit 1
            fi
        fi

        sleep "$delay"
        if [ "$exponential" = "true" ]; then
            delay=$((delay * 2))
        fi
    fi
done
exit 1