#!/usr/bin/env python3
"""Fail unless a BenchmarkBoard transcript satisfies the generality board's rules.

The rules, from the casei Initiative:

- every family's aggregate x_vs_best is below 1.0. Cells weigh equally within a
  family, so the aggregate is the mean of the cells' x_vs_best: casei's total
  time over the fastest entrant's total time once each cell is normalized to its
  fastest entrant;
- no cell has x_vs_best above 1.10;
- the run measured exactly the cells pinned in arena/board/cells.txt, from
  the pinned seed;
- casei answers every cell, and every pinned entrant that supports it was
  timed and counted. An entrant the run reports as wrong fails it: the board
  plants only fold mates every entrant handles, so a wrong answer is a board or
  adapter bug;
- the run is on the performance host: GenuineIntel family 6 model 143 with
  AVX-512F/BW/VBMI.

A transcript holds one run: one seed, one sample per cell.
"""

import argparse
from collections import defaultdict
import math
from pathlib import Path
import re
import sys


PREFIX = "BenchmarkBoard/"
PINNED = Path(__file__).resolve().parent.parent / "arena" / "board" / "cells.txt"
CELLS_PER_FAMILY = 24
COUNT_LEVELS = ("1", "2", "3-4", "5-8", "9-16", "17-32", "33-64")
SIZE_LEVELS = {
    "64B-255B": (64, 255),
    "256B-4KiB": (256, 4 * 1024 - 1),
    "4KiB-64KiB": (4 * 1024, 64 * 1024 - 1),
    "64KiB-1MiB": (64 * 1024, 1024 * 1024 - 1),
    "1MiB-4MiB": (1024 * 1024, 4 * 1024 * 1024 - 1),
    "4MiB-16MiB": (4 * 1024 * 1024, 16 * 1024 * 1024),
}
# The board's families and levels, as package board draws them. The verifier
# keeps its own copy so a run that drops a cell cannot also drop its expectation.
FAMILIES = {
    "count": COUNT_LEVELS,
    "length": COUNT_LEVELS,
    "script": ("ascii", "nonascii"),
    "case": ("ci", "cs"),
    "corpus": ("prose", "code", "logs", "russian"),
    "density": ("none", "sparse", "medium", "dense"),
    "size": tuple(SIZE_LEVELS),
    "op": ("find", "each", "indexfold"),
}
ENTRANTS = ("regexp", "pcre2", "rure", "vectorscan", "stringzilla", "veloz", "rustac")
CELL_METRICS = (
    "x_vs_best",
    "competitors",
    "entrants",
    "candidate_supported",
    "ascii_tier",
    "matches",
    "bytes",
) + tuple(f"{e}_{m}" for e in ENTRANTS for m in ("active", "x", "wrong"))
AGGREGATE_LIMIT = 1.0
CELL_LIMIT = 1.10
HOST = {"vendor": "GenuineIntel", "family": "6", "model": "143",
        "avx512f": "1", "avx512bw": "1", "avx512vbmi": "1"}


class VerificationError(ValueError):
    pass


def per_level(family):
    levels = len(FAMILIES[family])
    return -(-CELLS_PER_FAMILY // levels)


def expected_keys():
    return {
        f"{family}/{level}/{index:02d}"
        for family, levels in FAMILIES.items()
        for level in levels
        for index in range(per_level(family))
    }


def load_pinned(path=PINNED):
    """Return (seed, cell names) from the pinned cells file."""
    seed, names = None, []
    for line in Path(path).read_text().splitlines():
        if not line or line.startswith("#"):
            continue
        if line.startswith("seed="):
            seed = int(line.split("=", 1)[1], 0)
        else:
            names.append(line.split()[0])
    if seed is None:
        raise VerificationError(f"{path}: no seed line")
    return seed, names


def level_range(label):
    if label in SIZE_LEVELS:
        return SIZE_LEVELS[label]
    lo, _, hi = label.partition("-")
    return int(lo), int(hi or lo)


def parse(path):
    """Return (header fields, {cell key: cell}) from a transcript."""
    header = None
    cells = {}
    with Path(path).open() as source:
        for line_number, line in enumerate(source, 1):
            where = f"{path}:{line_number}"
            if line.startswith("board: "):
                if header is not None:
                    raise VerificationError(f"{where}: more than one board run; verify one run at a time")
                header = dict(field.split("=", 1) for field in line.split()[1:] if "=" in field)
                continue
            fields = line.split()
            if not fields or not fields[0].startswith(PREFIX):
                continue
            name = re.sub(r"-[0-9]+$", "", fields[0])[len(PREFIX):]
            parts = name.split("/")
            if len(parts) != 4:
                raise VerificationError(f"{where}: malformed cell name {name!r}")
            key = "/".join(parts[:3])
            if key in cells:
                raise VerificationError(f"{where}: cell {key} appears twice")
            attrs = dict(item.split("=", 1) for item in parts[3].split(","))
            metrics = {}
            for metric in CELL_METRICS:
                try:
                    value = float(fields[fields.index(metric) - 1])
                except (ValueError, IndexError) as err:
                    raise VerificationError(f"{where}: missing or invalid {metric}") from err
                if not math.isfinite(value):
                    raise VerificationError(f"{where}: non-finite {metric}")
                metrics[metric] = value
            cells[key] = {"name": name, "family": parts[0], "level": parts[1], "attrs": attrs, **metrics}
    if header is None:
        raise VerificationError(f"{path}: no 'board: seed=...' line; the run cannot be reproduced")
    if "seed" not in header:
        raise VerificationError(f"{path}: the board line has no seed")
    return header, cells


def holds_level(cell):
    """Report whether a cell's drawn properties put it in its own level."""
    family, level, attrs = cell["family"], cell["level"], cell["attrs"]
    if family == "count":
        lo, hi = level_range(level)
        return lo <= int(attrs["n"]) <= hi
    if family == "length":
        return attrs["len"] == f"{level_range(level)[0]}-{level_range(level)[1]}"
    if family == "size":
        lo, hi = level_range(level)
        return lo <= int(attrs["size"]) <= hi
    field = {"script": "script", "case": "case", "corpus": "corpus",
             "density": "density", "op": "op"}[family]
    return attrs[field] == level


def supported_entrants(cell):
    """Return the entrants that support a cell, from field.yaml's tiers."""
    ascii_tier = cell["ascii_tier"] == 1
    single = int(cell["attrs"]["n"]) == 1
    out = {"regexp", "pcre2", "rure", "vectorscan", "stringzilla"}
    if ascii_tier:
        out.add("rustac")
        if single:
            out.add("veloz")
    return out


def check_cell(key, cell):
    """Return the rule failures of one cell."""
    failures = []
    if not holds_level(cell):
        failures.append(f"{key}: drawn properties {cell['attrs']} are outside its level")
    if cell["candidate_supported"] != 1:
        what = "a case-sensitive cell" if cell["attrs"].get("case") == "cs" else "this cell"
        failures.append(f"{key}: casei does not support {what}")
        return failures
    want = supported_entrants(cell)
    for entrant in ENTRANTS:
        active, wrong = cell[f"{entrant}_active"], cell[f"{entrant}_wrong"]
        if wrong:
            failures.append(f"{key}: {entrant} answered wrongly; a board or adapter bug")
        elif entrant in want and active != 1:
            failures.append(f"{key}: {entrant} dropped ({entrant}_active={active:g})")
        if entrant not in want and active + wrong != 0:
            failures.append(f"{key}: {entrant} counted outside its tier ({entrant}_active={active:g})")
    active = [e for e in ENTRANTS if cell[f"{e}_active"] == 1]
    if cell["competitors"] != len(active) or cell["entrants"] != len(active) + 1:
        failures.append(
            f"{key}: competitors={cell['competitors']:g} entrants={cell['entrants']:g}, "
            f"want {len(active)} and {len(active) + 1} from the active entrants"
        )
    ratio = cell["x_vs_best"]
    worst = max((cell[f"{e}_x"] for e in active), default=0)
    if ratio <= 0 or not math.isclose(ratio, worst, rel_tol=1e-6):
        failures.append(
            f"{key}: x_vs_best={ratio:g} is not the largest entrant ratio {worst:g}"
        )
    if ratio > CELL_LIMIT:
        failures.append(f"{key}: x_vs_best={ratio:.4f} is above the {CELL_LIMIT} cell limit")
    return failures


def verify(path, pinned=PINNED):
    """Return (summary lines, failures) for a transcript."""
    header, cells = parse(path)
    pinned_seed, pinned_names = load_pinned(pinned)
    failures = []
    for field, want in HOST.items():
        if header.get(field) != want:
            failures.append(f"host: {field}={header.get(field)}, want {want}")
    if int(header["seed"], 0) != pinned_seed:
        failures.append(f"seed={header['seed']} is not the pinned seed {pinned_seed:#x}")
    pinned_keys = {name.rsplit("/", 1)[0] for name in pinned_names}
    if len(pinned_names) != len(pinned_keys) or pinned_keys != expected_keys():
        failures.append(f"{pinned}: pinned cells do not form the board's families and levels")
    found, want_names = {c["name"] for c in cells.values()}, set(pinned_names)
    if found != want_names:
        failures.append(
            f"cell inventory differs from {pinned.name}: missing={sorted(want_names - found)}, "
            f"unexpected={sorted(found - want_names)}"
        )
    for key in sorted(cells):
        failures.extend(check_cell(key, cells[key]))

    by_family = defaultdict(list)
    for cell in cells.values():
        by_family[cell["family"]].append(cell)
    lines = [f"seed={header['seed']} cells={len(cells)}",
             f"{'family':<8} {'cells':>5} {'answered':>8} {'aggregate':>9} {'worst':>7}"]
    for family in FAMILIES:
        members = by_family.get(family, [])
        answered = [c["x_vs_best"] for c in members if c["candidate_supported"] == 1]
        aggregate = sum(answered) / len(answered) if answered else math.nan
        worst = max(answered, default=math.nan)
        lines.append(f"{family:<8} {len(members):>5} {len(answered):>8} {aggregate:>9.4f} {worst:>7.4f}")
        if not members:
            failures.append(f"{family}: no cells")
        elif len(answered) < len(members):
            failures.append(f"{family}: {len(members) - len(answered)} of {len(members)} cells unanswered by casei")
        elif aggregate >= AGGREGATE_LIMIT:
            failures.append(f"{family}: aggregate x_vs_best={aggregate:.4f} is not below {AGGREGATE_LIMIT}")
    return lines, failures


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("transcript", type=Path)
    args = parser.parse_args()
    try:
        lines, failures = verify(args.transcript)
    except (OSError, VerificationError) as err:
        print(f"FAIL: {err}", file=sys.stderr)
        raise SystemExit(1)
    print("\n".join(lines))
    if failures:
        print(f"FAIL: {len(failures)} rule failures", file=sys.stderr)
        for failure in failures:
            print(f"  {failure}", file=sys.stderr)
        raise SystemExit(1)
    print("PASS: every family aggregate below 1.0, no cell above 1.10, full field on every cell")


if __name__ == "__main__":
    main()
