#!/usr/bin/env python3
"""Measure real SSH/Tailcat pools with a separate loopback fixture process."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--seconds", type=int, default=30)
    parser.add_argument("--transport", choices=("ssh", "tailcat", "all"), default="all")
    args = parser.parse_args()
    assert 2 <= args.seconds <= 90
    args.out.mkdir(parents=True, exist_ok=True)
    results = []
    with tempfile.TemporaryDirectory(prefix="herdrx-capacity-") as temporary:
        binary = Path(temporary) / "capacity-test"
        subprocess.run(["go", "test", "-c", "-o", str(binary), "./internal/hostruntime"], cwd=ROOT, check=True)
        for transport in (("ssh", "tailcat") if args.transport == "all" else (args.transport,)):
            for count in (10, 50, 100):
                env = {**os.environ, "GOMAXPROCS": "4", "HERDRX_CAPACITY_HOSTS": str(count), "HERDRX_CAPACITY_TRANSPORT": transport, "HERDRX_CAPACITY_DURATION": f"{args.seconds}s"}
                result = subprocess.run([str(binary), "-test.run=^TestCapacityMeasured$", "-test.timeout=4m", "-test.v"], cwd=ROOT, env=env, text=True, capture_output=True, timeout=250)
                (args.out / f"{transport}-{count}.log").write_text(result.stdout + result.stderr)
                if result.returncode:
                    raise RuntimeError(f"{transport}/{count} failed; see its log")
                records = [line.removeprefix("CAPACITY_RESULT ") for line in result.stdout.splitlines() if line.startswith("CAPACITY_RESULT ")]
                assert len(records) == 1
                data = json.loads(records[0])
                results.append(data)
                (args.out / "results.json").write_text(json.dumps(results, indent=2) + "\n")
                print(f"{transport}: configured={count}, hot={data['active']}, peak dials={data['max_concurrent_dials']}, RSS={data['ready']['rss_mib']:.1f} MiB, CPU={data['steady_cpu_one_core_percent']:.2f}% of one core", flush=True)


if __name__ == "__main__":
    main()
