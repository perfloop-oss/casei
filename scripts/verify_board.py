#!/usr/bin/env python3
"""Fail unless a BenchmarkBoard transcript satisfies the generality board's rules.

- The run is on the performance host: GenuineIntel family 6 model 143 with
  AVX-512F/BW/VBMI.
- It measured exactly the cells pinned in arena/board/cells.txt.
- casei answers every cell, and every pinned entrant that supports a cell was
  timed. An entrant reported wrong fails the run: the board plants only fold
  mates every entrant handles, so a wrong answer is a board or adapter bug.
- Each cell's x_vs_best is its largest entrant ratio, and no cell is above 1.10.
- Every family's aggregate x_vs_best is below 1.0. Cells weigh equally, so the
  aggregate is the mean of the family's x_vs_best: casei's total time over the
  fastest entrant's, with each cell normalized to its fastest entrant.
"""

import argparse
from collections import defaultdict
import math
from pathlib import Path
import re
import sys


PINNED = Path(__file__).resolve().parent.parent / "arena" / "board" / "cells.txt"
ENTRANTS = ("regexp", "pcre2", "rure", "vectorscan", "stringzilla", "veloz", "rustac")
HOST = {"vendor": "GenuineIntel", "family": "6", "model": "143",
        "avx512f": "1", "avx512bw": "1", "avx512vbmi": "1"}
AGGREGATE_LIMIT = 1.0
CELL_LIMIT = 1.10


class VerificationError(ValueError):
    pass


def load_pinned(path=PINNED):
    """Return (seed, cell names) from the pinned cells file."""
    lines = [l for l in Path(path).read_text().splitlines() if l and not l.startswith("#")]
    return int(lines[0].removeprefix("seed="), 0), [l.split()[0] for l in lines[1:]]


def parse(path):
    """Return (header fields, {cell name: metrics}) from a transcript of one run."""
    header, cells = None, {}
    for number, line in enumerate(Path(path).read_text().splitlines(), 1):
        fields = line.split()
        if line.startswith("board: "):
            if header is not None:
                raise VerificationError(f"{path}:{number}: more than one run")
            header = dict(f.split("=", 1) for f in fields[1:] if "=" in f)
        elif fields and fields[0].startswith("BenchmarkBoard/"):
            name = re.sub(r"-[0-9]+$", "", fields[0]).removeprefix("BenchmarkBoard/")
            if name in cells:
                raise VerificationError(f"{path}:{number}: {name} appears twice")
            values = dict(zip(fields[3::2], fields[2::2]))  # unit -> value, after the name and N
            try:
                cells[name] = {unit: float(values[unit]) for unit in
                               ("x_vs_best", "candidate_supported", "ascii_tier",
                                *(f"{e}_{m}" for e in ENTRANTS for m in ("active", "x", "wrong")))}
            except (KeyError, ValueError) as err:
                raise VerificationError(f"{path}:{number}: missing or invalid metric {err}") from err
    if header is None or "seed" not in header:
        raise VerificationError(f"{path}: no 'board: seed=...' line")
    return header, cells


def supported(name, cell):
    """Return the entrants that support a cell, from field.yaml's tiers."""
    out = {"regexp", "pcre2", "rure", "vectorscan", "stringzilla"}
    if cell["ascii_tier"] == 1:
        out.add("rustac")
        if "/n=1," in name:
            out.add("veloz")
    return out


def check_cell(name, cell):
    """Return the rule failures of one cell."""
    if cell["candidate_supported"] != 1:
        return [f"{name}: casei does not support this cell"]
    failures, want = [], supported(name, cell)
    for e in ENTRANTS:
        if cell[f"{e}_wrong"]:
            failures.append(f"{name}: {e} answered wrongly; a board or adapter bug")
        elif cell[f"{e}_active"] != (e in want):
            failures.append(f"{name}: {e}_active={cell[f'{e}_active']:g}, want {int(e in want)}")
    worst = max((cell[f"{e}_x"] for e in ENTRANTS if cell[f"{e}_active"] == 1), default=0)
    if cell["x_vs_best"] <= 0 or not math.isclose(cell["x_vs_best"], worst, rel_tol=1e-6):
        failures.append(f"{name}: x_vs_best={cell['x_vs_best']:g} is not the largest entrant ratio {worst:g}")
    if cell["x_vs_best"] > CELL_LIMIT:
        failures.append(f"{name}: x_vs_best={cell['x_vs_best']:.4f} is above the {CELL_LIMIT} cell limit")
    return failures


def verify(path, pinned=PINNED):
    """Return (summary lines, failures) for a transcript."""
    header, cells = parse(path)
    seed, names = load_pinned(pinned)
    failures = [f"host: {k}={header.get(k)}, want {v}" for k, v in HOST.items() if header.get(k) != v]
    if int(header["seed"], 0) != seed:
        failures.append(f"seed={header['seed']} is not the pinned seed {seed:#x}")
    if set(cells) != set(names):
        failures.append(f"cells differ from {Path(pinned).name}: missing={sorted(set(names) - set(cells))}, "
                        f"unexpected={sorted(set(cells) - set(names))}")
    families = defaultdict(list)
    for name, cell in sorted(cells.items()):
        failures.extend(check_cell(name, cell))
        families[name.split("/")[0]].append(cell)
    lines = [f"seed={header['seed']} cells={len(cells)}", "family    cells  answered  aggregate"]
    for family, members in families.items():
        answered = [c["x_vs_best"] for c in members if c["candidate_supported"] == 1]
        aggregate = sum(answered) / len(answered) if answered else math.nan
        lines.append(f"{family:<9} {len(members):>5} {len(answered):>9} {aggregate:>10.4f}")
        if len(answered) < len(members):
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
        print("\n".join(f"  {f}" for f in failures), file=sys.stderr)
        raise SystemExit(1)
    print("PASS")


if __name__ == "__main__":
    main()
