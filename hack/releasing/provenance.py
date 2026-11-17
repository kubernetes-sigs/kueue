#!/usr/bin/env python3

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

"""SLSA v1 build provenance for Kueue staging artifacts.

predicate builds the provenance predicate that a staging build signs.
check compares a verified in-toto statement with the release tag it must match.
Peeling an annotated tag to its commit is the caller's job; check compares commits only.
"""

from __future__ import annotations

import argparse
import base64
import json
import re
import sys
from typing import Any, NamedTuple

# cosign may wrap attestations in the in-toto v0.1 statement header, so both versions are accepted.
STATEMENT_TYPES = frozenset({"https://in-toto.io/Statement/v1", "https://in-toto.io/Statement/v0.1"})
PREDICATE_TYPE = "https://slsa.dev/provenance/v1"
SOURCE_REPOSITORY = "https://github.com/kubernetes-sigs/kueue"
BUILD_TYPE_FRAGMENT = "RELEASE.md#staging-artifact-provenance-v1"
BUILD_CONFIG_SOURCE = "cloudbuild.yaml"
STAGING_PROJECT_SERVICE_ACCOUNT = "kueue-sa"

PASS = "PASS"
NOT_READY = "NOT_READY"
FAIL = "FAIL"
EXIT_CODES = {PASS: 0, NOT_READY: 2, FAIL: 1}

_COMMIT_RE = re.compile(r"[0-9a-f]{40}")
_SHA256_RE = re.compile(r"[0-9a-f]{64}")
_REF_RE = re.compile(r"[A-Za-z0-9._/+-]+")
# The SemVer 2.0 pattern of the image-builder postsubmit that builds tags, from the
# branches of post-kueue-push-images in kubernetes/test-infra,
# config/jobs/image-pushing/k8s-staging-kueue.yaml. A tag that matches it is built as
# refs/tags/<tag>, so it is the one this module accepts as a release tag. The release
# scripts (hack/releasing/wait_for_images.sh) accept a narrower set; keep them in mind.
_SEMVER_TAG_RE = re.compile(
    r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
    r"(?:-((?:0|[1-9][0-9]*|[0-9]*[a-zA-Z-][0-9a-zA-Z-]*)"
    r"(?:\.(?:0|[1-9][0-9]*|[0-9]*[a-zA-Z-][0-9a-zA-Z-]*))*))?"
    r"(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?"
)
_BUILD_ID_RE = re.compile(r"[A-Za-z0-9-]+")
_PROJECT_RE = re.compile(r"[a-z0-9-]+")
_PROJECT_NUMBER_RE = re.compile(r"[0-9]+")


class CheckResult(NamedTuple):
    status: str
    reason: str


def is_semver_tag(ref: str) -> bool:
    """Return whether ref names a release tag, such as v0.21.0 or v0.21.0-rc.1."""
    return _SEMVER_TAG_RE.fullmatch(ref) is not None


def source_ref(ref: str) -> str:
    """Return the full git ref that a build of ref records."""
    if is_semver_tag(ref):
        return f"refs/tags/{ref}"
    return f"refs/heads/{ref}"


def build_type(commit: str) -> str:
    """Return the buildType URI, pinned to the runbook at commit.

    The URI is deliberately version-pinned: it names the commit and the runbook
    fragment. BUILD_TYPE_FRAGMENT must match the `Staging artifact provenance v1`
    heading in RELEASE.md. Do not change either: check() would then reject
    already-signed artifacts. A different predicate shape needs a new vN section
    and a new fragment.
    """
    return f"{SOURCE_REPOSITORY}/blob/{commit}/{BUILD_TYPE_FRAGMENT}"


def _validate_ref(ref: str) -> None:
    """Reject values that are not a plain branch or tag name.

    Beyond the character set, this rejects the git ref forms that would make the
    recorded source URI ambiguous or that git itself refuses.
    """
    if (
        _REF_RE.fullmatch(ref) is None
        or ".." in ref
        or "//" in ref
        or ref[0] in "-./"
        or ref.endswith("/")
        or ref.startswith("refs/")
    ):
        raise ValueError(f"invalid ref: {ref!r}")


def builder_id(project: str) -> str:
    """Return the Cloud Build identity that signs the predicate."""
    service_account = f"{STAGING_PROJECT_SERVICE_ACCOUNT}@{project}.iam.gserviceaccount.com"
    return (
        f"https://cloudbuild.googleapis.com/projects/{project}"
        f"/serviceAccounts/{service_account}/{BUILD_CONFIG_SOURCE}"
    )


def predicate(
    ref: str,
    commit: str,
    build_id: str,
    project: str,
    project_number: str,
) -> dict[str, Any]:
    """Return the SLSA v1 provenance predicate for a build of ref at commit.

    ref is the branch or tag that triggered the build, such as main or v0.21.0.
    project is the Cloud Build project ID, and project_number is its number,
    which the invocation URL uses.
    """
    _validate_ref(ref)
    _require(_COMMIT_RE, commit, "commit (40 lowercase hex characters)")
    _require(_BUILD_ID_RE, build_id, "build ID")
    _require(_PROJECT_RE, project, "project")
    _require(_PROJECT_NUMBER_RE, project_number, "project number")

    return {
        "buildDefinition": {
            "buildType": build_type(commit),
            "externalParameters": {
                "source": {
                    "uri": f"git+{SOURCE_REPOSITORY}@{source_ref(ref)}",
                    "digest": {"gitCommit": commit},
                },
            },
            "internalParameters": {"buildConfigSource": BUILD_CONFIG_SOURCE},
            "resolvedDependencies": [
                {
                    "uri": f"git+{SOURCE_REPOSITORY}",
                    "digest": {"gitCommit": commit},
                },
            ],
        },
        "runDetails": {
            "builder": {"id": builder_id(project)},
            "metadata": {
                "invocationId": (
                    f"https://console.cloud.google.com/cloud-build/builds/{build_id}"
                    f"?project={project_number}"
                ),
            },
        },
    }


def check(payload: str | dict[str, Any], tag: str, commit: str) -> CheckResult:
    """Classify a verified in-toto statement against the release tag.

    payload is the statement as a JSON object, JSON text, or base64 of JSON text.
    tag is a release tag name, such as v0.21.0.
    commit is the commit the tag points to; for an annotated tag, pass the peeled commit.

    Returns PASS when the statement records the tag and commit, NOT_READY when it
    records a different ref or commit, and FAIL when it is malformed or has the
    wrong predicateType or buildType. A statement must have exactly one subject:
    one attestation covers one digest.
    Raises ValueError when tag or commit is not valid.
    """
    if not is_semver_tag(tag):
        raise ValueError(f"tag must be a release tag such as v0.21.0, got {tag!r}")
    _require(_COMMIT_RE, commit, "commit (40 lowercase hex characters)")

    try:
        statement = load_statement(payload)
    except ValueError as err:
        return CheckResult(FAIL, f"malformed payload: {err}")

    if statement.get("predicateType") != PREDICATE_TYPE:
        return CheckResult(
            FAIL,
            f"predicateType is {statement.get('predicateType')!r}, want {PREDICATE_TYPE!r}",
        )

    try:
        digests, uri, git_commit, found_build_type = _statement_fields(statement)
    except ValueError as err:
        return CheckResult(FAIL, f"malformed statement: {err}")

    if found_build_type != build_type(git_commit):
        return CheckResult(
            FAIL,
            f"buildType {found_build_type!r} does not match commit {git_commit}",
        )

    expected_uri = f"git+{SOURCE_REPOSITORY}@refs/tags/{tag}"
    seen = f"{' '.join(digests)} ref {uri.partition('@')[2]} commit {git_commit}"
    if uri == expected_uri and git_commit == commit:
        return CheckResult(PASS, seen)
    # Any other ref or commit means the build for the current tag has not finished.
    # Callers keep waiting and fail only on timeout; this is not a permanent failure.
    return CheckResult(
        NOT_READY,
        f"{seen}, expected ref refs/tags/{tag} commit {commit}",
    )


def load_statement(payload: str | dict[str, Any]) -> dict[str, Any]:
    """Return the in-toto statement as a dict from a dict, JSON text, or base64 JSON."""
    if isinstance(payload, dict):
        return payload
    if not isinstance(payload, str):
        raise ValueError(f"payload must be a JSON object or a string, got {type(payload).__name__}")
    text = payload.strip()
    if not text.startswith("{"):
        # Base64 from cosign can be wrapped at 76 columns, and some tools drop padding.
        compact = "".join(text.split())
        compact += "=" * (-len(compact) % 4)
        try:
            text = base64.b64decode(compact, validate=True).decode("utf-8")
        except ValueError as err:
            raise ValueError(f"not JSON or base64 of JSON: {err}") from err
    try:
        statement = json.loads(text)
    except (ValueError, RecursionError) as err:
        # Deeply nested input overflows the parser's stack and raises RecursionError.
        raise ValueError(f"invalid JSON: {err}") from err
    if not isinstance(statement, dict):
        raise ValueError("statement is not a JSON object")
    return statement


def _statement_fields(statement: dict[str, Any]) -> tuple[list[str], str, str, str]:
    """Return the subject digests, source URI, gitCommit and buildType of a statement."""
    statement_type = _field(statement, "_type")
    if not isinstance(statement_type, str) or statement_type not in STATEMENT_TYPES:
        raise ValueError(f"_type is {statement_type!r}, want one of {sorted(STATEMENT_TYPES)}")

    subjects = _field(statement, "subject")
    if not isinstance(subjects, list) or len(subjects) != 1:
        raise ValueError("subject must be a list with exactly one entry")
    digests = []
    for subject in subjects:
        if not isinstance(subject, dict):
            raise ValueError("subject entry must be an object")
        digest = _field(subject, "digest", "sha256")
        if not isinstance(digest, str):
            raise ValueError("subject digest sha256 must be a string")
        if _SHA256_RE.fullmatch(digest) is None:
            raise ValueError("subject digest sha256 must be 64 lowercase hex characters")
        digests.append(f"sha256:{digest}")

    build_definition = _field(statement, "predicate", "buildDefinition")
    build_type_value = _field(build_definition, "buildType")
    source = _field(build_definition, "externalParameters", "source")
    uri = _field(source, "uri")
    git_commit = _field(source, "digest", "gitCommit")
    if not isinstance(uri, str) or not isinstance(build_type_value, str):
        raise ValueError("buildType and source uri must be strings")
    if not isinstance(git_commit, str) or _COMMIT_RE.fullmatch(git_commit) is None:
        raise ValueError("source gitCommit must be 40 lowercase hex characters")
    return digests, uri, git_commit, build_type_value


def _field(obj: Any, *keys: str) -> Any:
    """Return obj[key1][key2]... or raise ValueError naming the missing key."""
    for key in keys:
        if not isinstance(obj, dict) or key not in obj:
            raise ValueError(f"missing field {key!r}")
        obj = obj[key]
    return obj


def _require(pattern: re.Pattern[str], value: str, name: str) -> None:
    if not isinstance(value, str) or pattern.fullmatch(value) is None:
        raise ValueError(f"invalid {name}: {value!r}")


class _Parser(argparse.ArgumentParser):
    def error(self, message: str) -> None:
        # Exit 2 means NOT_READY for check, so usage errors exit 1 like FAIL.
        self.print_usage(sys.stderr)
        self.exit(1, f"{self.prog}: error: {message}\n")


def _parser() -> argparse.ArgumentParser:
    parser = _Parser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    subparsers = parser.add_subparsers(dest="command", required=True)

    predicate_parser = subparsers.add_parser(
        "predicate", help="print the SLSA v1 provenance predicate as JSON"
    )
    predicate_parser.add_argument("--ref", required=True, help="branch or tag that triggered the build")
    predicate_parser.add_argument("--commit", required=True, help="full commit SHA of the build")
    predicate_parser.add_argument("--build-id", required=True, help="Cloud Build build ID")
    predicate_parser.add_argument("--project", required=True, help="Cloud Build project ID")
    predicate_parser.add_argument("--project-number", required=True, help="Cloud Build project number")

    check_parser = subparsers.add_parser(
        "check",
        help="compare a verified statement with a release tag",
        description=(
            "Exit 0 when the statement records the tag and commit (PASS), 2 when it "
            "records a different ref or commit (NOT_READY), and 1 when it is malformed "
            "or has the wrong predicateType or buildType (FAIL)."
        ),
    )
    check_parser.add_argument("--tag", required=True, help="release tag, such as v0.21.0")
    check_parser.add_argument(
        "--commit",
        required=True,
        help="commit the tag points to; for an annotated tag, the peeled commit",
    )
    check_parser.add_argument(
        "--payload",
        default="-",
        help="file with the statement payload, as JSON or base64 of JSON; - reads stdin",
    )
    return parser


def main(argv: list[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    try:
        if args.command == "predicate":
            result = predicate(
                args.ref, args.commit, args.build_id, args.project, args.project_number
            )
            print(json.dumps(result, indent=2))
            return 0

        if args.payload == "-":
            payload = sys.stdin.read()
        else:
            with open(args.payload, encoding="utf-8") as payload_file:
                payload = payload_file.read()
        result = check(payload, args.tag, args.commit)
    except (OSError, ValueError) as err:
        print(f"error: {err}", file=sys.stderr)
        return 1

    print(f"{result.status}: {result.reason}")
    return EXIT_CODES[result.status]


if __name__ == "__main__":
    raise SystemExit(main())
