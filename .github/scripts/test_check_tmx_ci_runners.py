import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from check_tmx_ci_runners import main


def run(workflows: dict[str, str], extra_files: dict[str, str] | None = None, exceptions=()) -> int:
    """workflows: filename -> content, written under a throwaway .github/workflows/.
    extra_files: repo-root-relative path -> content, for scripts a job calls."""
    with tempfile.TemporaryDirectory() as tmp:
        wf = Path(tmp) / ".github" / "workflows"
        wf.mkdir(parents=True)
        for name, content in workflows.items():
            (wf / name).write_text(content, encoding="utf-8")
        for rel, content in (extra_files or {}).items():
            p = Path(tmp) / rel
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_text(content, encoding="utf-8")
        import os
        cwd = os.getcwd()
        os.chdir(tmp)
        try:
            with patch("check_tmx_ci_runners.SELF_HOSTED_BUILD_JOBS", frozenset(exceptions)):
                return main()
        finally:
            os.chdir(cwd)


BASE = """\
name: x
jobs:
  test-job:
    runs-on: {runs_on}
    steps:
      - run: {run_line}
"""


class CheckTmxCiRunnersTests(unittest.TestCase):
    def test_tagged_go_test_on_github_hosted_passes(self):
        wf = BASE.format(runs_on="windows-latest", run_line="go test -tags tmx_ci ./...")
        self.assertEqual(run({"x.yml": wf}), 0)

    def test_untagged_go_test_on_self_hosted_fails(self):
        # the case the rule exists for: !windows || tmx_ci means the guard tests compile and
        # run on macOS WITHOUT the tag - an untagged go test on tmx-mac must fail just as hard.
        wf = BASE.format(runs_on="tmx-mac", run_line="go test ./...")
        self.assertNotEqual(run({"x.yml": wf}), 0)

    def test_make_check_on_self_hosted_fails(self):
        wf = BASE.format(runs_on="tmx-mac", run_line="make lint typecheck vet test")
        self.assertNotEqual(run({"x.yml": wf}), 0)

    def test_make_race_on_self_hosted_fails(self):
        wf = BASE.format(runs_on="tmx-mac", run_line="make race")
        self.assertNotEqual(run({"x.yml": wf}), 0)

    def test_called_script_with_go_test_on_self_hosted_fails(self):
        wf = BASE.format(runs_on="tmx-mac", run_line="bash scripts/x.sh")
        extra = {"scripts/x.sh": "#!/bin/bash\ngo test ./...\n"}
        self.assertNotEqual(run({"x.yml": wf}, extra), 0)

    def test_called_script_without_go_test_on_self_hosted_fails(self):
        wf = BASE.format(runs_on="tmx-mac", run_line="bash scripts/x.sh")
        extra = {"scripts/x.sh": "#!/bin/bash\necho building\ngo build ./...\n"}
        self.assertNotEqual(run({"x.yml": wf}, extra), 0)

    def test_unreadable_called_script_fails_closed(self):
        wf = BASE.format(runs_on="tmx-mac", run_line="bash scripts/missing.sh")
        self.assertNotEqual(run({"x.yml": wf}), 0)

    def test_checkout_path_alias_resolves_called_script(self):
        # actions/checkout with path: tools re-checks this same repo out under tools/, so
        # tools/scripts/x.sh in a run: line is really scripts/x.sh in this repo's own tree -
        # same shape as public-mirror.yml's tools/scripts/public-scrub.sh.
        wf = """\
name: x
jobs:
  mirror:
    runs-on: tmx-mac
    steps:
      - uses: actions/checkout@v4
        with: { path: tools, ref: "${{ github.sha }}" }
      - run: bash tools/scripts/x.sh
"""
        extra = {"scripts/x.sh": "#!/bin/bash\ngo test ./...\n"}
        self.assertNotEqual(run({"x.yml": wf}, extra), 0)

    def test_build_only_job_on_self_hosted_mac_needs_explicit_exception(self):
        wf = BASE.format(runs_on="tmx-mac", run_line="make app WAILS=wails SIGN_IDENTITY=-")
        self.assertNotEqual(run({"x.yml": wf}), 0)
        self.assertEqual(run({"x.yml": wf}, exceptions={(".github/workflows/x.yml", "test-job")}), 0)

    def test_dynamic_matrix_build_without_tests_fails(self):
        # whisper-engine.yml's real build job: runs-on fed by a prior job's fromJSON output,
        # cannot prove its runners even if its visible command only builds.
        wf = """\
name: x
jobs:
  build:
    strategy:
      matrix: ${{ fromJSON(needs.pin.outputs.matrix) }}
    runs-on: ${{ matrix.os }}
    steps:
      - run: cmake --build .
"""
        self.assertNotEqual(run({"x.yml": wf}), 0)

    def test_composite_action_and_make_all_on_self_hosted_fail(self):
        wf = """\
jobs:
  sneaky:
    runs-on: [self-hosted, tmx-mac]
    steps:
      - uses: ./.github/actions/run-tests
      - run: make all
"""
        self.assertNotEqual(run({"x.yml": wf}), 0)

    def test_indirect_commands_on_self_hosted_fail_without_guessing(self):
        for command in ("make all", "npm test", "task test", "just test", "./suite.test", "echo build"):
            with self.subTest(command=command):
                wf = BASE.format(runs_on="tmx-mac", run_line=command)
                self.assertNotEqual(run({"x.yml": wf}), 0)

    def test_exception_is_bound_to_both_workflow_and_job(self):
        wf = BASE.format(runs_on="tmx-mac", run_line="go build ./...")
        for entry in ((".github/workflows/other.yml", "test-job"), (".github/workflows/x.yml", "other")):
            with self.subTest(entry=entry):
                self.assertNotEqual(run({"x.yml": wf}, exceptions={entry}), 0)

    def test_build_exception_cannot_run_tests_or_local_actions(self):
        exception = {(".github/workflows/x.yml", "test-job")}
        for command in ("go test ./...", "make test", "make check", "make race", "bash missing.sh"):
            with self.subTest(command=command):
                wf = BASE.format(runs_on="tmx-mac", run_line=command)
                self.assertNotEqual(run({"x.yml": wf}, exceptions=exception), 0)
        wf = BASE.format(runs_on="tmx-mac", run_line="go build ./...")
        wf += "      - uses: ./.github/actions/x\n"
        self.assertNotEqual(run({"x.yml": wf}, exceptions=exception), 0)

    def test_reusable_workflow_without_runner_is_refused(self):
        wf = "jobs:\n  indirect:\n    uses: ./.github/workflows/tests.yml\n"
        self.assertNotEqual(run({"x.yml": wf}), 0)
        wf += "    with:\n      script: |\n        runs-on: ubuntu-latest\n"
        self.assertNotEqual(run({"x.yml": wf}), 0)

    def test_yaml_extension_is_checked(self):
        wf = BASE.format(runs_on="tmx-mac", run_line="make all")
        self.assertNotEqual(run({"x.yaml": wf}), 0)

    def test_empty_jobs_mapping_fails_closed(self):
        self.assertNotEqual(run({"x.yml": "jobs:\n"}), 0)

    def test_test_job_with_inline_literal_matrix_all_hosted_passes(self):
        # o5's final rule: an inline literal matrix passes a test-running job when every value
        # it can take is GitHub-hosted - matrix and expression are treated the same way.
        wf = """\
name: x
jobs:
  test:
    strategy:
      matrix: {os: [ubuntu-latest, windows-latest]}
    runs-on: ${{ matrix.os }}
    steps:
      - run: go test ./...
"""
        self.assertEqual(run({"x.yml": wf}), 0)

    def test_test_job_with_inline_literal_matrix_one_self_hosted_fails(self):
        wf = """\
name: x
jobs:
  test:
    strategy:
      matrix: {os: [ubuntu-latest, tmx-mac]}
    runs-on: ${{ matrix.os }}
    steps:
      - run: go test ./...
"""
        self.assertNotEqual(run({"x.yml": wf}), 0)

    def test_test_job_with_ternary_of_hosted_literals_passes(self):
        # an expression passes a test-running job when it reduces entirely to quoted literals
        # and every one is GitHub-hosted. A condition (inputs.mac here) may reference anything
        # freely, since it only picks between arms, never becomes the result. Same shape as
        # build.yml's real test job.
        wf = """\
name: x
jobs:
  test:
    runs-on: ${{ (github.event_name == 'workflow_dispatch' && inputs.mac) && 'macos-15' || 'ubuntu-latest' }}
    steps:
      - run: make lint typecheck vet test
"""
        self.assertEqual(run({"x.yml": wf}), 0)

    def test_test_job_with_dynamic_ref_in_an_arm_fails(self):
        # unlike inputs.mac as a condition above, vars.RUNNER here IS an arm (the ternary's
        # result), so it could directly supply the runner name - must fail regardless of what
        # it might evaluate to.
        wf = """\
name: x
jobs:
  test:
    runs-on: ${{ github.event_name == 'push' && vars.RUNNER || 'ubuntu-latest' }}
    steps:
      - run: make lint typecheck vet test
"""
        self.assertNotEqual(run({"x.yml": wf}), 0)

    def test_test_job_with_matrix_ref_in_an_arm_fails(self):
        wf = """\
name: x
jobs:
  test:
    runs-on: ${{ github.event_name == 'push' && matrix.os || 'ubuntu-latest' }}
    steps:
      - run: go test ./...
"""
        self.assertNotEqual(run({"x.yml": wf}), 0)

    def test_test_job_with_non_literal_arm_fails(self):
        # an arm that isn't even a quoted literal (here, a bare identifier) fails closed too,
        # not just a recognized dynamic-reference pattern.
        wf = """\
name: x
jobs:
  test:
    runs-on: ${{ github.event_name == 'push' && something_unquoted || 'ubuntu-latest' }}
    steps:
      - run: go test ./...
"""
        self.assertNotEqual(run({"x.yml": wf}), 0)

    def test_test_job_with_ternary_resolving_to_self_hosted_fails(self):
        wf = """\
name: x
jobs:
  test:
    runs-on: ${{ github.event_name == 'push' && 'tmx-mac' || 'ubuntu-latest' }}
    steps:
      - run: go test ./...
"""
        self.assertNotEqual(run({"x.yml": wf}), 0)

    def test_no_workflow_files_fails_closed(self):
        self.assertNotEqual(run({}), 0)

    def test_unparseable_workflow_fails_closed(self):
        self.assertNotEqual(run({"x.yml": "name: not a real workflow\non: push\n"}), 0)

    def test_no_test_running_job_anywhere_is_fine(self):
        # a workflow set can legitimately have zero test-running jobs; this isn't itself a
        # fail-closed signal (the zero-files/unparseable-file checks cover a scrub going silent).
        wf = BASE.format(runs_on="ubuntu-latest", run_line="echo hi")
        self.assertEqual(run({"x.yml": wf}), 0)


if __name__ == "__main__":
    unittest.main()
