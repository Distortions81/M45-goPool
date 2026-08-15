#!/usr/bin/env python3
"""Start one pool under open-pool-benchmark and leave it running for Go probes."""

from __future__ import annotations

import argparse
import dataclasses

from openbench import adapters, config, runner


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("pool")
    parser.add_argument("--profile", default="validation")
    parser.add_argument("--no-pin", action="store_true")
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    registry = config.load_registry("pools.yml")
    if args.no_pin:
        registry = dataclasses.replace(
            registry,
            pinning=dataclasses.replace(registry.pinning, enabled=False),
        )

    spec = registry.pool(args.pool)
    profile = registry.profile(args.profile)
    with runner.session(registry, keep=True) as run:
        run.backend.ensure_wallet()
        with adapters.PoolUnderTest(run, spec, profile, keep=True):
            pass

    print(f"pool {args.pool} is running")


if __name__ == "__main__":
    main()
