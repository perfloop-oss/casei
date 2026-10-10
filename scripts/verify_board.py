#!/usr/bin/env python3
"""Fail unless a BenchmarkBoard transcript satisfies the generality board's rules.

- The run is on Sapphire Rapids: GenuineIntel family 6 model 143 with
  AVX-512F/BW/VBMI.
- It measured exactly the cells pinned in arena/board/cells.txt, or with
  --family, exactly that family's cells, for iterating on one family; the
  full board is the acceptance check.
- casei answers every cell, and every pinned entrant that supports a cell was
  timed. (BenchmarkBoard fails a cell outright on any wrong answer.)
- A cell's x_vs_best is its largest entrant ratio, casei's time over that of
  the fastest entrant; no cell is above 1.10.
- Every family's aggregate x_vs_best is below 1.0. Cells weigh equally, so the
  aggregate is the mean of the family's x_vs_best: casei's total time over the
  fastest entrants', with each cell normalized to its fastest entrant.
"""

import argparse
from collections import defaultdict
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
    """Return (seed, {cell name: tier}) from the pinned cells file."""
    seed, tiers = None, {}
    for line in Path(path).read_text().splitlines():
        if line.startswith("seed="):
            seed = int(line[5:], 0)
        elif line and not line.startswith("#"):
            name, tier, _ = line.split()
            tiers[name] = tier
    return seed, tiers


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
                cells[name] = {u: float(values[u]) for u in
                               ("candidate_supported", *(f"{e}_{m}" for e in ENTRANTS for m in ("active", "x")))}
            except (KeyError, ValueError) as err:
                raise VerificationError(f"{path}:{number}: missing or invalid metric {err}") from err
    if header is None or "seed" not in header:
        raise VerificationError(f"{path}: no 'board: seed=...' line")
    return header, cells


def supported(name, tier):
    """Return the entrants that support a cell, from field.yaml's tiers."""
    out = {"regexp", "pcre2", "rure", "vectorscan", "stringzilla"}
    if tier == "ascii":
        out.add("rustac")
        if "/n=1," in name:
            out.add("veloz")
    return out


def verify(path, pinned=PINNED, family=None):
    """Return (summary lines, failures) for a transcript of the board, or of
    one family when family is set."""
    header, cells = parse(path)
    seed, tiers = load_pinned(pinned)
    if family is not None:
        if family not in {n.split("/")[0] for n in tiers}:
            raise VerificationError(f"--family {family!r} is not a pinned family")
        tiers = {n: t for n, t in tiers.items() if n.split("/")[0] == family}
    failures = [f"host: {k}={header.get(k)}, want {v}" for k, v in HOST.items() if header.get(k) != v]
    if int(header["seed"], 0) != seed:
        failures.append(f"seed={header['seed']} is not the pinned seed {seed:#x}")
    if not cells:
        failures.append("the run has no cells")
    if set(cells) != set(tiers):
        failures.append(f"cells differ from {Path(pinned).name}: missing={sorted(set(tiers) - set(cells))}, "
                        f"unexpected={sorted(set(cells) - set(tiers))}")
    families = defaultdict(list)
    for name, cell in sorted(cells.items()):
        x = None
        if cell["candidate_supported"] != 1:
            failures.append(f"{name}: casei does not support this cell")
        else:
            want = supported(name, tiers.get(name))
            failures += [f"{name}: {e}_active={cell[f'{e}_active']:g}, want {int(e in want)}"
                         for e in ENTRANTS if cell[f"{e}_active"] != (e in want)]
            x = max((cell[f"{e}_x"] for e in ENTRANTS if cell[f"{e}_active"] == 1), default=0)
            if not 0 < x <= CELL_LIMIT:
                failures.append(f"{name}: x_vs_best={x:.4f} is outside (0, {CELL_LIMIT}]")
        families[name.split("/")[0]].append(x)
    lines = [f"seed={header['seed']} cells={len(cells)}", "family    cells  aggregate x_vs_best"]
    for family, xs in families.items():
        if None in xs:
            failures.append(f"{family}: {xs.count(None)} of {len(xs)} cells unanswered by casei")
            lines.append(f"{family:<9} {len(xs):>5}  unanswered")
            continue
        aggregate = sum(xs) / len(xs)
        lines.append(f"{family:<9} {len(xs):>5}  {aggregate:.4f}")
        if aggregate >= AGGREGATE_LIMIT:
            failures.append(f"{family}: aggregate x_vs_best={aggregate:.4f} is not below {AGGREGATE_LIMIT}")
    return lines, failures


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("transcript", type=Path)
    parser.add_argument("--family", help="verify one family's run instead of the whole board")
    args = parser.parse_args()
    try:
        lines, failures = verify(args.transcript, family=args.family)
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
