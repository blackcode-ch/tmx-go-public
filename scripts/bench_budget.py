"""Gate RSI storage microbenchmarks; preserve failures in bench.json as well as exit status."""

import json
import math
from pathlib import Path
import re
import sys


# Initial, deliberately generous regression ceilings for shared hosted runners.
# Change these explicitly in review, never auto-update from the current result.
BUDGETS = {
    "BenchmarkRSIMessageRoundTrip": {"ns/op": 5_000_000, "B/op": 65_536, "allocs/op": 500},
    "BenchmarkRSIIdleInboxPoll": {"ns/op": 1_000_000, "B/op": 16_384, "allocs/op": 200},
}
SAMPLES = 3
ITERATIONS = 1000
PACKAGE = "github.com/blackcode-ch/tmx-go/internal/store"


def evaluate(raw):
    report = {
        "scope": "storage microbenchmarks only; no daemon or network started",
        "budgets": BUDGETS,
        "samples": {name: [] for name in BUDGETS},
        "todo": ["end-to-end message delivery p95", "whole-daemon idle CPU and RSS"],
        "errors": [],
    }
    errors = report["errors"]
    output = []
    passed = False
    try:
        for line in raw.splitlines():
            event = json.loads(line)
            if event.get("Action") == "fail":
                errors.append("Go benchmark process reported failure")
            if event.get("Package") != PACKAGE:
                continue
            if event.get("Action") == "pass" and not event.get("Test"):
                passed = True
            output.append(event.get("Output", ""))
        for line in "".join(output).splitlines():
            fields = line.split()
            if not fields:
                continue
            name = re.sub(r"-\d+$", "", fields[0])
            if name not in BUDGETS:
                continue
            # test2json emits a bare name when a benchmark starts, before its
            # CPU-suffixed result lines. This is not a measurement.
            if len(fields) == 1 and fields[0] in BUDGETS:
                continue
            if len(fields) != 8 or int(fields[1]) != ITERATIONS:
                raise ValueError("unexpected benchmark format or iteration count")
            values = {fields[i + 1]: float(fields[i]) for i in (2, 4, 6)}
            if set(values) != set(BUDGETS[name]):
                raise ValueError("missing or unknown benchmark units")
            if any(not math.isfinite(v) or v < 0 for v in values.values()):
                raise ValueError("invalid benchmark value")
            report["samples"][name].append(values)
    except (ValueError, TypeError, AttributeError) as exc:
        errors.append(f"invalid benchmark input: {exc}")
    if not passed:
        errors.append("missing successful Go package completion")
    for name, budgets in BUDGETS.items():
        samples = report["samples"][name]
        if len(samples) != SAMPLES:
            errors.append(f"{name}: expected {SAMPLES} samples, got {len(samples)}")
        for sample in samples:
            for unit, limit in budgets.items():
                if sample[unit] > limit:
                    errors.append(f"{name}: {sample[unit]} {unit} exceeds {limit}")
    report["passed"] = not errors
    return report


def main():
    source, destination = map(Path, sys.argv[1:])
    try:
        report = evaluate(source.read_text(encoding="utf-8"))
    except OSError as exc:
        report = evaluate("")
        report["errors"].append(f"cannot read benchmark input: {exc}")
    destination.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    for error in report["errors"]:
        print(error, file=sys.stderr)
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
