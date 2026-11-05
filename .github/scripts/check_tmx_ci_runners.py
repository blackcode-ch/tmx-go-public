"""Require a GitHub-hosted runner for every workflow job, regardless of its commands.

architect's rule: guard tests (and the ACL-mutating aclmutate suite) never run on a
person's real machine. Indirect tests through actions, scripts, build targets or
reusable workflows cannot be identified reliably, so they never exempt a job.
Self-hosted build/sign jobs need an explicit (workflow path, job id) entry below.
There are currently no such exceptions. Exceptions still reject test commands,
unreadable scripts and local actions.

A job's runs-on must resolve entirely to GitHub-hosted labels: a static
literal, a list, an inline literal matrix (`matrix: {os: [...]}`), or a `${{ cond &&
'A' || cond2 && 'B' || ... }}` ternary expression - as long as every value it can
resolve to is a GitHub-hosted literal. A condition (the cond in `cond && 'A'`) may
contain anything, since it only picks between arms and is never itself the result. A
dynamic matrix (fromJSON(...), a prior job's output), a bare non-literal expression
arm, or anything else that doesn't reduce to a literal fails closed. Likewise a called
script that can't be read, zero workflow files, or an unparseable workflow.
"""

import re
import sys
from pathlib import Path

ALLOWED = frozenset({
    "ubuntu-latest", "ubuntu-22.04", "ubuntu-22.04-arm", "ubuntu-24.04", "ubuntu-24.04-arm",
    "windows-latest", "windows-2022", "windows-2025",
    "macos-latest", "macos-14", "macos-15",
})
# Any addition needs security review of this exact workflow/job. No job needs one today.
SELF_HOSTED_BUILD_JOBS: frozenset[tuple[str, str]] = frozenset()
LOCAL_USES = re.compile(r"\buses:\s*['\"]?\./")
JOB_HEADER = re.compile(r"^  (\S+):\s*$")
RUNS_ON = re.compile(r"^    runs-on:\s*(.+?)\s*$")
GO_TEST_OR_GOTESTSUM = re.compile(r"\bgo\s+test\b|\bgotestsum\b")
# `make` can take several space-separated targets on one line (`make lint typecheck vet test`),
# so a test/race/check target can appear anywhere after `make`, not just glued to it.
MAKE_CALL = re.compile(r"\bmake\b(?P<targets>(?:\s+\S+)*)")
MAKE_TEST_TARGET = re.compile(r"\b(?:test|race|check)\w*\b")
SCRIPT_CALL = re.compile(r"(?:^|[|;&]|\brun:\s)\s*(?:bash|sh|python3?)?\s*"
                          r"(\.?/?[\w./-]+\.(?:sh|py))\b")


class NoJobsSection(Exception):
    """path has no top-level jobs: mapping - almost certainly not a real workflow file."""


class UnreadableScript(Exception):
    """a run: line calls a script this can't read - fail closed rather than skip it."""


def jobs_in(path: Path):
    """Yield (job_name, lines) for each top-level job block in a workflow's jobs: section."""
    lines = path.read_text(encoding="utf-8").splitlines()
    try:
        start = next(i for i, l in enumerate(lines) if l.rstrip() == "jobs:") + 1
    except StopIteration:
        raise NoJobsSection(path)
    name, block = None, []
    for line in lines[start:]:
        if line and not line[0].isspace():
            break  # end of the jobs: mapping
        m = JOB_HEADER.match(line)
        if m:
            if name is not None:
                yield name, block
            name, block = m.group(1), []
        elif name is not None:
            block.append(line)
    if name is not None:
        yield name, block


CHECKOUT_PATH = re.compile(r"^\s*with:.*\bpath:\s*([\w./-]+)")


def checkout_aliases(lines):
    """path: aliases this job's own actions/checkout steps declare (e.g. `with: {path: tools,
    ref: github.sha}` checks this same repo out again under tools/) - a script called as
    tools/x is really x in this repo's own tree, not a real tools/ directory on disk here."""
    aliases = set()
    for i, line in enumerate(lines):
        if "actions/checkout" not in line:
            continue
        for fl in lines[i:i + 4]:
            m = CHECKOUT_PATH.search(fl)
            if m:
                aliases.add(m.group(1).strip("/"))
                break
    return aliases


def called_script_paths(lines):
    """Paths any run: line in this job block invokes (bash/sh/python x, or a bare ./x)."""
    return {m.group(1) for line in lines for m in SCRIPT_CALL.finditer(line)}


def resolve_script(rel: str, aliases: set[str], repo_root: Path) -> Path:
    """rel as a path under repo_root, stripping a leading checkout path: alias if rel has one
    (tools/scripts/x.sh -> scripts/x.sh when `tools` is one of this job's checkout aliases)."""
    parts = Path(rel.lstrip("./")).parts
    if parts and parts[0] in aliases:
        return repo_root / Path(*parts[1:])
    return repo_root / rel


def has_test_signal(text: str) -> bool:
    """go test, gotestsum, or a make call with a test/race/check target among its (possibly
    several, space-separated) targets - anywhere on the line, not just glued to `make`."""
    if GO_TEST_OR_GOTESTSUM.search(text):
        return True
    return any(MAKE_TEST_TARGET.search(m.group("targets")) for m in MAKE_CALL.finditer(text))


def runs_tests(lines, repo_root: Path) -> bool:
    """True if this job's run: lines, or a script any of them calls, run tests."""
    if has_test_signal("\n".join(lines)):
        return True
    aliases = checkout_aliases(lines)
    for rel in called_script_paths(lines):
        p = resolve_script(rel, aliases, repo_root)
        try:
            content = p.read_text(encoding="utf-8")
        except OSError as e:
            raise UnreadableScript(f"{rel}: {e}")
        if has_test_signal(content):
            return True
    return False


QUOTED_LITERAL = re.compile(r"^'([^']*)'$|^\"([^\"]*)\"$")


def split_top_level(expr: str, sep: str) -> list[str]:
    """expr split on sep (&& or ||) where it appears outside any parens."""
    depth, parts, buf, i = 0, [], "", 0
    while i < len(expr):
        c = expr[i]
        if c in "([":
            depth += 1
        elif c in ")]":
            depth -= 1
        if depth == 0 and expr[i:i + len(sep)] == sep:
            parts.append(buf)
            buf = ""
            i += len(sep)
            continue
        buf += c
        i += 1
    parts.append(buf)
    return [p.strip() for p in parts]


def ternary_arms(expr: str):
    """The possible result arms of a `cond && 'A' || cond2 && 'B' || ... || 'Z'` expression -
    each `||`-separated segment's part after its LAST top-level &&, or the whole segment if it
    has none (the final, unconditional fallback arm). Conditions themselves are discarded."""
    for segment in split_top_level(expr, "||"):
        pieces = split_top_level(segment, "&&")
        yield pieces[-1]  # whatever follows the last &&, or the segment itself if there's none


def resolve_expression(expr: str):
    """Literal runner labels expr's arms resolve to, or None if the expression doesn't reduce
    entirely to quoted literals (any arm that isn't a pure quoted literal - a dynamic
    reference like vars.RUNNER or matrix.os, or anything else - fails closed). Conditions
    (the cond in `cond && 'A'`) are never arms, so they may contain anything."""
    labels = []
    for arm in ternary_arms(expr):
        m = QUOTED_LITERAL.match(arm)
        if not m:
            return None
        labels.append(m.group(1) or m.group(2))
    return labels


MATRIX_KEY = re.compile(r"^\s*matrix:\s*(.+?)\s*$")
MATRIX_FIELD = re.compile(r"^\s*(\S+):\s*(\[.+\]|.+)\s*$")
MATRIX_VAR = re.compile(r"^\$\{\{\s*matrix\.(\w+)\s*\}\}$")


def literal_matrix_values(lines, var):
    """Inline literal values for strategy.matrix.<var> in this job block, if there are any
    (e.g. `matrix: {os: [ubuntu-latest, macos-14]}` or a block-style `os:` list under
    `matrix:`); None if the matrix is itself dynamic (fromJSON(...), a prior job's output, ...)
    rather than a literal list - that fails closed, same as any other unresolvable value."""
    for i, line in enumerate(lines):
        m = MATRIX_KEY.match(line)
        if not m:
            continue
        rest = m.group(1)
        if rest.startswith("${{") or "fromJSON" in rest:
            return None
        fm = re.search(rf"{re.escape(var)}:\s*\[([^\]]*)\]", rest)
        if fm:
            return [v.strip(" '\"") for v in fm.group(1).split(",") if v.strip()]
        for j in range(i + 1, len(lines)):
            fl = lines[j]
            if not fl.strip():
                continue
            if not fl.startswith("      "):
                break
            fm2 = MATRIX_FIELD.match(fl)
            if fm2 and fm2.group(1) == var:
                val = fm2.group(2)
                if val.startswith("["):
                    return [v.strip(" '\"]") for v in val.strip("[]").split(",") if v.strip()]
                if val.startswith("${{") or "fromJSON" in val:
                    return None
                items = [val.strip(" '\"")] if val.strip() else []
                for k in range(j + 1, len(lines)):
                    il = lines[k]
                    if il.strip().startswith("-"):
                        items.append(il.strip()[1:].strip(" '\""))
                    elif il.strip():
                        break
                return items or None
        return None
    return None


def static_runs_on(raw, lines):
    """Literal runner label(s) raw could resolve to, or None if it can't be trusted: a list, a
    matrix var whose values aren't inline literals in this job, or a `${{ ... }}` expression
    that doesn't reduce entirely to quoted literals."""
    raw = raw.strip()
    if raw.startswith("["):
        return [v.strip(" '\"") for v in raw.strip("[]").split(",") if v.strip()]
    m = MATRIX_VAR.match(raw)
    if m:
        return literal_matrix_values(lines, m.group(1))
    if raw.startswith("${{") and raw.endswith("}}"):
        return resolve_expression(raw[3:-2].strip())
    return [raw.strip(" '\"")]


def main() -> int:
    repo_root = Path(".")
    # Fail closed: a scrub, rename or path change that leaves this with nothing to check (no
    # workflow files or an unparseable one) must not look like a clean pass.
    workflows = repo_root / ".github/workflows"
    paths = sorted(p for p in workflows.iterdir() if p.suffix in (".yml", ".yaml")) if workflows.is_dir() else []
    if not paths:
        print("no .github/workflows/*.yml files found - can't check anything", file=sys.stderr)
        return 1

    failed = False
    for path in paths:
        try:
            jobs = list(jobs_in(path))
        except NoJobsSection:
            print(f"{path}: no top-level jobs: mapping found", file=sys.stderr)
            failed = True
            continue
        if not jobs:
            print(f"{path}: no parseable jobs found", file=sys.stderr)
            failed = True
        for job, lines in jobs:
            exception = (path.as_posix(), job) in SELF_HOSTED_BUILD_JOBS
            if exception:
                try:
                    unsafe = runs_tests(lines, repo_root) or any(LOCAL_USES.search(l) for l in lines)
                except UnreadableScript:
                    unsafe = True
                if unsafe:
                    print(f"{path}: build/sign exception {job!r} contains tests, a local action, "
                          "or an unreadable script", file=sys.stderr)
                    failed = True
                    continue

            raw = next((RUNS_ON.match(l).group(1) for l in lines if RUNS_ON.match(l)), None)
            if raw is None:
                print(f"{path}: job {job!r} has no runs-on; reusable workflows need explicit "
                      "runner verification", file=sys.stderr)
                failed = True
                continue
            labels = static_runs_on(raw, lines)
            if not labels:
                print(f"{path}: job {job!r} has runs-on {raw!r} - a matrix, list, "
                      "or an expression with a non-literal or dynamically-sourced arm, isn't "
                      "trusted to always resolve to a GitHub-hosted runner, even if one "
                      "branch of it does", file=sys.stderr)
                failed = True
                continue
            for label in labels:
                if label not in ALLOWED and not exception:
                    print(f"{path}: job {job!r} can run on {label!r} (from "
                          f"runs-on {raw!r}), not an approved GitHub-hosted runner", file=sys.stderr)
                    failed = True

    # Every parsed job must comply; missing files and empty/unparseable jobs fail above.
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
