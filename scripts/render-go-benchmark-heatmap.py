#!/usr/bin/env python3
"""Render the Go benchmark JSONL report as benchmarks/go/heatmap.svg."""

from __future__ import annotations

import argparse
import datetime as dt
import html
import json
import math
from pathlib import Path
from typing import Any


DEFAULT_POOLS = ["gopool", "pogolo", "ckpool", "warppool", "public-pool"]
DEFAULT_MINERS = [100, 1000, 10000]
GOOD = (24, 143, 74)
MID = (242, 215, 121)
BAD = (217, 47, 39)
FAILED = "#7f1d1d"


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("input", help="JSONL file written by scripts/run-go-benchmarks.sh")
    parser.add_argument(
        "-o",
        "--output",
        default="benchmarks/go/heatmap.svg",
        help="SVG output path",
    )
    parser.add_argument(
        "--title-date",
        default="",
        help="Date label to show in the SVG subtitle, defaults to metadata date",
    )
    return parser.parse_args()


def read_records(path: Path) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    metadata: dict[str, Any] = {}
    records: list[dict[str, Any]] = []
    for line in path.read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        row = json.loads(line)
        if row.get("kind") == "metadata":
            metadata.update(row)
        elif row.get("kind") in {"submit", "notify"}:
            records.append(row)
    return metadata, records


def metric_value(record: dict[str, Any], key: str) -> float:
    if key == "validated_per_sec":
        return float(record[key])
    if key in {"avg", "p50", "p95", "p99", "max"}:
        return float(record[key])
    raise KeyError(key)


def fmt_value(value: float, throughput: bool = False) -> str:
    if throughput:
        return f"{value:,.0f}"
    if abs(value) >= 1000:
        return f"{value:.1f}"
    if abs(value) >= 100:
        return f"{value:.1f}"
    if abs(value) >= 10:
        return f"{value:.1f}"
    rounded = f"{value:.3f}".rstrip("0").rstrip(".")
    return rounded or "0"


def lerp(a: int, b: int, t: float) -> int:
    return round(a + (b - a) * t)


def color_for(value: float, lo: float, hi: float, higher_is_better: bool) -> str:
    if not math.isfinite(value) or hi <= lo:
        rgb = GOOD
    else:
        pos = (value - lo) / (hi - lo)
        if higher_is_better:
            pos = 1.0 - pos
        pos = max(0.0, min(1.0, pos))
        if pos <= 0.5:
            t = pos / 0.5
            rgb = tuple(lerp(GOOD[i], MID[i], t) for i in range(3))
        else:
            t = (pos - 0.5) / 0.5
            rgb = tuple(lerp(MID[i], BAD[i], t) for i in range(3))
    return f"#{rgb[0]:02x}{rgb[1]:02x}{rgb[2]:02x}"


def text_class(hex_color: str) -> str:
    r = int(hex_color[1:3], 16)
    g = int(hex_color[3:5], 16)
    b = int(hex_color[5:7], 16)
    luminance = (0.299 * r + 0.587 * g + 0.114 * b) / 255
    return "dark" if luminance < 0.55 else "light"


def panel_records(
    records: list[dict[str, Any]],
    pools: list[str],
    miners: list[int],
    kind: str,
    mode: str | None = None,
) -> list[dict[str, Any]]:
    by_key = {
        (row["pool"], int(row["miners"])): row
        for row in records
        if row.get("kind") == kind and (mode is None or row.get("mode") == mode)
    }
    ordered: list[dict[str, Any]] = []
    missing: list[str] = []
    for pool in pools:
        for miner_count in miners:
            row = by_key.get((pool, miner_count))
            if row is None:
                missing.append(f"{kind}/{mode or '-'} {pool} {miner_count}")
            else:
                ordered.append(row)
    if missing:
        raise SystemExit("missing benchmark records: " + ", ".join(missing))
    return ordered


def svg_text(x: int, y: int, cls: str, text: str) -> str:
    return f'<text x="{x}" y="{y}" class="{cls}">{html.escape(text)}</text>'


def render_panel(
    title: str,
    rows: list[dict[str, Any]],
    pools: list[str],
    miners: list[int],
    metrics: list[tuple[str, str, bool]],
    y_offset: int,
    log_scale: bool = False,
) -> list[str]:
    out: list[str] = [f'  <g transform="translate(0 {y_offset})">']
    out.append(svg_text(34, 0, "panel", title))
    out.append(svg_text(96, 52, "head", "pool / miners"))
    for i, (label, _, _) in enumerate(metrics):
        out.append(svg_text(238 + i * 118, 52, "head", label))

    scales: dict[str, tuple[float, float]] = {}
    for _, key, _ in metrics:
        values = [
            math.log10(max(metric_value(row, key), 1e-9)) if log_scale else metric_value(row, key)
            for row in rows
            if row.get("status") != "failed"
        ]
        if not values:
            values = [0.0]
        scales[key] = (min(values), max(values))

    y = 66
    for pool_index, pool in enumerate(pools):
        if pool_index:
            y += 8
        for miner_count in miners:
            row = next(
                r
                for r in rows
                if r["pool"] == pool and int(r["miners"]) == miner_count
            )
            out.append(svg_text(34, y + 16, "row", f"{pool} {miner_count}"))
            if row.get("status") == "failed":
                out.append(f'    <rect x="179" y="{y}" width="590" height="32" fill="{FAILED}"/>')
                reason = str(row.get("reason", "benchmark failed"))
                label = "N/A*" if "connection cap is 4096" in reason else f"FAIL — {reason}"
                out.append(svg_text(474, y + 16, "cell dark", label))
                y += 32
                continue
            for i, (_, key, higher_is_better) in enumerate(metrics):
                x = 179 + i * 118
                value = metric_value(row, key)
                lo, hi = scales[key]
                color_value = math.log10(max(value, 1e-9)) if log_scale else value
                color = color_for(color_value, lo, hi, higher_is_better)
                out.append(f'    <rect x="{x}" y="{y}" width="118" height="32" fill="{color}"/>')
                out.append(
                    svg_text(
                        x + 59,
                        y + 16,
                        f"cell {text_class(color)}",
                        fmt_value(value, throughput=(key == "validated_per_sec")),
                    )
                )
            y += 32
    out.append("  </g>")
    return out


def main() -> None:
    args = parse_args()
    metadata, records = read_records(Path(args.input))
    run_date = args.title_date or metadata.get("date") or dt.datetime.now(dt.UTC).date().isoformat()
    result_profile = str(metadata.get("result_profile", "legacy unlabeled profile"))
    if run_date and len(run_date) == 10:
        run_date = dt.date.fromisoformat(run_date).strftime("%B %-d, %Y")

    pools = [str(pool) for pool in metadata.get("pools", DEFAULT_POOLS)]
    miners = [int(miner_count) for miner_count in metadata.get("miners", DEFAULT_MINERS)]
    submit = panel_records(records, pools, miners, "submit")
    notify_zmq = panel_records(records, pools, miners, "notify", "zmq")
    notify_nozmq = panel_records(records, pools, miners, "notify", "no-zmq")

    panel_height = 58 + (32 * len(miners) + 8) * len(pools)
    panel_offsets = [
        96,
        96 + panel_height + 46,
        96 + panel_height * 2 + 76,
    ]
    footnote_y = panel_offsets[2] + panel_height + 34
    svg_height = footnote_y + 40

    lines = [
        f'<svg xmlns="http://www.w3.org/2000/svg" width="820" height="{svg_height}" viewBox="0 0 820 {svg_height}" role="img" aria-labelledby="title desc">',
        '  <title id="title">goPool benchmark heat map</title>',
        f'  <desc id="desc">Heat map of mining.submit and mining.notify benchmark results from the {html.escape(str(run_date))} rerun using the {html.escape(result_profile)}. Each numeric metric column is colored independently; failed cells are labeled explicitly.</desc>',
        "  <style>",
        '    text { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; fill: #111827; }',
        "    .title { font-size: 26px; font-weight: 700; }",
        "    .subtitle { font-size: 14px; fill: #4b5563; }",
        "    .panel { font-size: 18px; font-weight: 700; }",
        "    .head { font-size: 12px; font-weight: 700; fill: #374151; text-anchor: middle; }",
        "    .row { font-size: 12px; font-weight: 650; dominant-baseline: middle; }",
        "    .cell { font-size: 12px; font-weight: 750; text-anchor: middle; dominant-baseline: middle; }",
        "    .light { fill: #111827; }",
        "    .dark { fill: #ffffff; }",
        "  </style>",
        f'  <rect width="820" height="{svg_height}" fill="#ffffff"/>',
        '  <text x="34" y="42" class="title">Benchmark heat map</text>',
        f'  <text x="34" y="66" class="subtitle">{html.escape(result_profile.title())} · rerun {html.escape(str(run_date))}. Green is better; red is worse.</text>',
        '  <g transform="translate(560 30)">',
        '    <rect x="0" y="0" width="44" height="16" fill="#188f4a"/>',
        '    <rect x="44" y="0" width="44" height="16" fill="#88b462"/>',
        '    <rect x="88" y="0" width="44" height="16" fill="#f2d779"/>',
        '    <rect x="132" y="0" width="44" height="16" fill="#ea9257"/>',
        '    <rect x="176" y="0" width="44" height="16" fill="#d92f27"/>',
        '    <text x="0" y="32" class="subtitle">better</text>',
        '    <text x="176" y="32" class="subtitle">worse</text>',
        "  </g>",
    ]
    lines += render_panel(
        "mining.submit",
        submit,
        pools,
        miners,
        [
            ("valid/s", "validated_per_sec", True),
            ("p50", "p50", False),
            ("p95", "p95", False),
            ("p99", "p99", False),
            ("max", "max", False),
        ],
        panel_offsets[0],
        log_scale=True,
    )
    lines += render_panel(
        "mining.notify, ZMQ/default",
        notify_zmq,
        pools,
        miners,
        [
            ("avg", "avg", False),
            ("p50", "p50", False),
            ("p95", "p95", False),
            ("p99", "p99", False),
            ("max", "max", False),
        ],
        panel_offsets[1],
        log_scale=True,
    )
    lines += render_panel(
        "mining.notify, no pool-side ZMQ",
        notify_nozmq,
        pools,
        miners,
        [
            ("avg", "avg", False),
            ("p50", "p50", False),
            ("p95", "p95", False),
            ("p99", "p99", False),
            ("max", "max", False),
        ],
        panel_offsets[2],
        log_scale=True,
    )
    lines.append(svg_text(34, footnote_y, "subtitle", "* WarpPool's stock Enterprise profile has a 4,096-connection hard cap."))
    lines.append(svg_text(34, footnote_y + 22, "subtitle", "Colors use a logarithmic scale so outliers do not flatten the panels."))
    lines.append("</svg>")

    output = Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text("\n".join(lines) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
