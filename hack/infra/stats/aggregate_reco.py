#!/usr/bin/env python3

# Copyright The Kubernetes Authors.
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

"""
aggregate_reco.py — combine every <workdir>/*/recommendation.json (written by
plot.py) into a single recommendation.md table in the work directory.

The table shows, per job, the current vs recommended CPU/memory request and limit,
the CPU cores saved by the recommendation, and a TOTAL row summing the savings.

Example:
  ./aggregate_reco.py ./artifacts/infra-stats
"""
import argparse, glob, json, os


def num(v):
    return "?" if v is None else f"{v:g}"


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("workdir", help="work directory holding the per-job <job>_*/ folders")
    ap.add_argument("--out", default=None, help="output path (default <workdir>/recommendation.md)")
    args = ap.parse_args()

    files = sorted(glob.glob(os.path.join(args.workdir, "*", "recommendation.json")))
    rows = []
    for f in files:
        with open(f) as fh:
            rows.append(json.load(fh))
    if not rows:
        raise SystemExit(f"no recommendation.json under {args.workdir}/*/ — run the plot scripts first")

    header = ("| job | builds | avg dur (min) | cur cpu req | cur cpu lim | cur mem req | cur mem lim "
              "| rec cpu req | rec cpu lim | rec mem req | rec mem lim |")
    sep = "|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|"
    lines = [f"# CPU/memory right-sizing recommendations ({len(rows)} jobs)",
             "", "CPU values are cores; memory values are GiB.", "",
             header, sep]

    # accumulate potential savings per metric: sum(current - recommended)
    saved = {"cpu_req": 0.0, "cpu_lim": 0.0, "mem_req": 0.0, "mem_lim": 0.0}

    # the CPU target build duration (minutes) all jobs were sized against
    target_mins = {r["cpu"].get("stats", {}).get("target_min") for r in rows}
    target_mins.discard(None)

    def add(key, cur, rec):
        if cur is not None and rec is not None:
            saved[key] += cur - rec

    for r in rows:
        c, m = r["cpu"], r["mem"]
        add("cpu_req", c["request_current"], c["request_recommended"])
        add("cpu_lim", c["limit_current"], c["limit_recommended"])
        add("mem_req", m["request_current"], m["request_recommended"])
        add("mem_lim", m["limit_current"], m["limit_recommended"])
        lines.append(
            f"| {r['job']} | {r['builds']} | {num(r.get('avg_duration_min'))} "
            f"| {num(c['request_current'])} | {num(c['limit_current'])} "
            f"| {num(m['request_current'])} | {num(m['limit_current'])} "
            f"| {num(c['request_recommended'])} | {num(c['limit_recommended'])} "
            f"| {num(m['request_recommended'])} | {num(m['limit_recommended'])} |")

    lines += [
        "",
        f"Potentially saved cpu req: {saved['cpu_req']:g} cores",
        f"Potentially saved cpu lim: {saved['cpu_lim']:g} cores",
        f"Potentially saved mem req: {saved['mem_req']:g} GiB",
        f"Potentially saved mem lim: {saved['mem_lim']:g} GiB",
        "",
        f"Target duration: {', '.join(f'{t:g}' for t in sorted(target_mins))} min"
        if target_mins else "Target duration: ? min",
    ]

    out = args.out or os.path.join(args.workdir, "recommendation.md")
    with open(out, "w") as f:
        f.write("\n".join(lines) + "\n")
    print(f"{len(rows)} jobs -> {out}")
    for k, unit in (("cpu_req", "cores"), ("cpu_lim", "cores"), ("mem_req", "GiB"), ("mem_lim", "GiB")):
        print(f"  saved {k}: {saved[k]:g} {unit}")


if __name__ == "__main__":
    main()
