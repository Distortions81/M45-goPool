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


POOLS = ["gopool", "pogolo", "ckpool"]
MINERS = [100, 1000, 10000]
GOOD = (24, 143, 74)
MID = (242, 215, 121)
BAD = (217, 47, 39)


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
    records: list[dict[str, Any]], kind: str, mode: str | None = None
) -> list[dict[str, Any]]:
    by_key = {
        (row["pool"], int(row["miners"])): row
        for row in records
        if row.get("kind") == kind and (mode is None or row.get("mode") == mode)
    }
    ordered: list[dict[str, Any]] = []
    missing: list[str] = []
    for pool in POOLS:
        for miners in MINERS:
            row = by_key.get((pool, miners))
            if row is None:
                missing.append(f"{kind}/{mode or '-'} {pool} {miners}")
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
    metrics: list[tuple[str, str, bool]],
    y_offset: int,
) -> list[str]:
    out: list[str] = [f'  <g transform="translate(0 {y_offset})">']
    out.append(svg_text(34, 0, "panel", title))
    out.append(svg_text(34, 52, "head", "pool / miners"))
    for i, (label, _, _) in enumerate(metrics):
        out.append(svg_text(238 + i * 118, 52, "head", label))

    scales: dict[str, tuple[float, float]] = {}
    for _, key, _ in metrics:
        values = [metric_value(row, key) for row in rows]
        scales[key] = (min(values), max(values))

    y = 66
    for pool_index, pool in enumerate(POOLS):
        if pool_index:
            y += 8
        for miners in MINERS:
            row = next(r for r in rows if r["pool"] == pool and int(r["miners"]) == miners)
            out.append(svg_text(34, y + 16, "row", f"{pool} {miners}"))
            for i, (_, key, higher_is_better) in enumerate(metrics):
                x = 179 + i * 118
                value = metric_value(row, key)
                lo, hi = scales[key]
                color = color_for(value, lo, hi, higher_is_better)
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
    if run_date and len(run_date) == 10:
        run_date = dt.date.fromisoformat(run_date).strftime("%B %-d, %Y")

    submit = panel_records(records, "submit")
    notify_zmq = panel_records(records, "notify", "zmq")
    notify_nozmq = panel_records(records, "notify", "no-zmq")

    lines = [
        '<svg xmlns="http://www.w3.org/2000/svg" width="820" height="1320" viewBox="0 0 820 1320" role="img" aria-labelledby="title desc">',
        '  <title id="title">goPool benchmark heat map</title>',
        f'  <desc id="desc">Heat map of all numeric values in the mining.submit and mining.notify benchmark tables from the {html.escape(str(run_date))} rerun. Each metric column is colored independently; green is better and red is worse.</desc>',
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
        '  <rect width="820" height="1320" fill="#ffffff"/>',
        '  <text x="34" y="42" class="title">Benchmark heat map</text>',
        f'  <text x="34" y="66" class="subtitle">Rerun {html.escape(str(run_date))}. Every numeric table value is a tile; green is better, red is worse.</text>',
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
        [
            ("valid/s", "validated_per_sec", True),
            ("p50", "p50", False),
            ("p95", "p95", False),
            ("p99", "p99", False),
            ("max", "max", False),
        ],
        96,
    )
    lines += render_panel(
        "mining.notify, ZMQ/default",
        notify_zmq,
        [
            ("avg", "avg", False),
            ("p50", "p50", False),
            ("p95", "p95", False),
            ("p99", "p99", False),
            ("max", "max", False),
        ],
        520,
    )
    lines += render_panel(
        "mining.notify, no pool-side ZMQ",
        notify_nozmq,
        [
            ("avg", "avg", False),
            ("p50", "p50", False),
            ("p95", "p95", False),
            ("p99", "p99", False),
            ("max", "max", False),
        ],
        928,
    )
    lines.append("</svg>")

    output = Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text("\n".join(lines) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
