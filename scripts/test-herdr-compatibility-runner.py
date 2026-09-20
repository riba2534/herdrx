#!/usr/bin/env python3
"""Regression checks for the gate: skipped or absent tests must never turn green."""
import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("gate", Path(__file__).with_name("test-herdr-compatibility.py"))
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)


class GateTests(unittest.TestCase):
    required = {"package": ["TestReal"]}

    def events(self, action="pass"):
        return [{"Package": "package", "Test": "TestReal", "Action": action},
                {"Package": "package", "Action": "pass"}]

    def test_executed_test_passes(self):
        self.assertEqual(gate.verify_events(self.events(), self.required, 0), 1)

    def test_skipped_required_test_fails(self):
        with self.assertRaises(RuntimeError):
            gate.verify_events(self.events("skip"), self.required, 0)

    def test_no_matching_test_fails(self):
        with self.assertRaises(RuntimeError):
            gate.verify_events([{"Package": "package", "Action": "pass"}], self.required, 0)

    def test_skipped_subtest_fails_even_with_passing_parent(self):
        with self.assertRaises(RuntimeError):
            gate.verify_events(self.events() + [{"Package": "package", "Test": "TestReal/pixels", "Action": "skip"}], self.required, 0)

    def test_failed_package_or_process_fails(self):
        for events, code in [(self.events()[:-1], 0), (self.events(), 1)]:
            with self.subTest(events=events, code=code), self.assertRaises(RuntimeError):
                gate.verify_events(events, self.required, code)

    def test_corrupt_cache_fails_before_execution(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / "0.9.1/linux-amd64/herdr"
            path.parent.mkdir(parents=True)
            path.write_bytes(b"not an upstream executable")
            manifest = {"versions": {"0.9.1": {"linux-amd64": {"sha256": "0" * 64}}}}
            with self.assertRaisesRegex(RuntimeError, "checksum mismatch"):
                gate.pinned_binary(manifest, "0.9.1", "linux-amd64", Path(folder))


if __name__ == "__main__":
    unittest.main()
