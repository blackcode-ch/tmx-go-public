import json
import unittest

from bench_budget import BUDGETS, PACKAGE, evaluate


def fixture():
    events = []
    for name in BUDGETS:
        for _ in range(3):
            events.append({"Package": PACKAGE, "Action": "output", "Output":
                           f"{name}-2\t1000\t100 ns/op\t100 B/op\t10 allocs/op\n"})
    events.append({"Package": PACKAGE, "Action": "pass"})
    return events


def check(events):
    return evaluate("\n".join(json.dumps(event) for event in events))


class BudgetTests(unittest.TestCase):
    def test_pass(self):
        self.assertTrue(check(fixture())["passed"])

    def test_each_budget_enforced(self):
        for unit, old, new in [("ns/op", "100 ns/op", "6000000 ns/op"),
                               ("B/op", "100 B/op", "70000 B/op"),
                               ("allocs/op", "10 allocs/op", "501 allocs/op")]:
            with self.subTest(unit=unit):
                events = fixture()
                events[0]["Output"] = events[0]["Output"].replace(old, new)
                self.assertFalse(check(events)["passed"])

    def test_missing_or_failed_samples(self):
        for events in [[], fixture()[1:], fixture()[:-1],
                       fixture() + [{"Action": "fail"}]]:
            self.assertFalse(check(events)["passed"])

    def test_bad_values_and_shape(self):
        for replacement in ["NaN ns/op", "-1 ns/op", "Inf ns/op", "100 unknown"]:
            events = fixture()
            events[0]["Output"] = events[0]["Output"].replace("100 ns/op", replacement)
            self.assertFalse(check(events)["passed"])
        self.assertFalse(evaluate("not json")["passed"])

    def test_split_output(self):
        events = fixture()
        first = events.pop(0)
        left, right = first["Output"].split("\t", 1)
        events[:0] = [{**first, "Output": left + "\t"}, {**first, "Output": right}]
        self.assertTrue(check(events)["passed"])

    def test_go_benchmark_start_labels(self):
        events = fixture()
        for name in BUDGETS:
            events.insert(0, {"Package": PACKAGE, "Action": "output", "Test": name,
                              "Output": name + "\n"})
        self.assertTrue(check(events)["passed"])


if __name__ == "__main__":
    unittest.main()
