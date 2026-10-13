#!/usr/bin/env python3
"""Fail unless a BenchmarkBoard transcript satisfies the generality board's rules.

- The run is on Sapphire Rapids: GenuineIntel family 6 model 143 with
  AVX-512F/BW/VBMI.
- It measured exactly the cells pinned in arena/board/cells.txt, or with
  --family, exactly that family's cells, for iterating on one family; the
  full board is the acceptance check.
- casei answers every cell, case-sensitive cells included, and every pinned
  entrant that supports a cell was timed: on a case-sensitive cell, every
  entrant with an exact mode. (BenchmarkBoard fails a cell outright on any
  wrong answer, so a wrong answer leaves its cell out of the run.)
- Every timed entrant's ratio is positive and finite. A cell's x_vs_best is
  its largest entrant ratio, casei's time over that of the fastest entrant; no
  cell is above 1.10.
- Every family's aggregate x_vs_best is below 1.0. Cells weigh equally, so the
  aggregate is the mean of the family's x_vs_best: casei's total time over the
  fastest entrants', with each cell normalized to its fastest entrant.

With --master and --change, it compares runs of master and of a change on one
host instead, the landing check for a performance change. Run each at least
twice, alternating (master, change, change, master), so drift between runs
falls on both. Every run must be a valid run of the same cells; the cell limit
and the aggregate bar do not apply. For each family, it prints both aggregates,
their difference and its noise bound, and a verdict: slower or faster when the
difference exceeds the bound, otherwise same. A slower family fails. The noise
is each cell's variance between runs of the same code, so it costs no extra
runs and a real effect cannot widen it. When cells' noise is independent, a
change with no effect fails some family with probability at most ALPHA (5%).

The bound shrinks as 1/sqrt(runs). In a noisy family it can be larger than a
real regression, so read the bound, not only the verdict. Tighten it only by
adding alternating runs; never drop, replace or re-run runs until a comparison
passes.

The bound assumes cells' noise is independent within a run. If a run moves a
family's cells together, the bound is too narrow and changes with no effect
fail more often, and adding runs does not fix that. To check it, run the same
code several times: a family's cells times the variance of its run aggregates,
over the mean of its cells' variances, should be close to 1.
"""

import argparse
from collections import defaultdict
import math
from pathlib import Path
import re
from statistics import fmean, variance
import sys


PINNED = Path(__file__).resolve().parent.parent / "arena" / "board" / "cells.txt"
ENTRANTS = ("regexp", "pcre2", "rure", "vectorscan", "stringzilla", "veloz", "rustac")
HOST = {"vendor": "GenuineIntel", "family": "6", "model": "143",
        "avx512f": "1", "avx512bw": "1", "avx512vbmi": "1"}
AGGREGATE_LIMIT = 1.0
CELL_LIMIT = 1.10
ALPHA = 0.05  # chance that compare fails a change with no effect


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
    """Return the entrants that support a cell, from field.yaml's tiers and,
    on a case-sensitive cell, its exact_tier."""
    out = {"regexp", "pcre2", "rure", "vectorscan", "stringzilla"}
    if name.endswith(",case=cs"):
        return out | {"rustac"}
    if tier == "ascii":
        out.add("rustac")
        if "/n=1," in name:
            out.add("veloz")
    return out


def measure(path, pinned=PINNED, family=None):
    """Return (header fields, {cell name: x_vs_best, or None where casei does
    not answer}, failures) for a transcript of the board, or of one family
    when family is set. The failures are the run's validity: host, seed,
    pinned cells, casei's answers, and the timed field, with every ratio
    positive and finite."""
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
    xs = {}
    for name, cell in sorted(cells.items()):
        xs[name] = None
        if cell["candidate_supported"] != 1:
            failures.append(f"{name}: casei does not support this cell")
            continue
        want = supported(name, tiers.get(name))
        failures += [f"{name}: {e}_active={cell[f'{e}_active']:g}, want {int(e in want)}"
                     for e in ENTRANTS if cell[f"{e}_active"] != (e in want)]
        ratios = {e: cell[f"{e}_x"] for e in ENTRANTS if cell[f"{e}_active"] == 1}
        failures += [f"{name}: {e}_x={r:g} is not a positive finite ratio"
                     for e, r in ratios.items() if not 0 < r < math.inf]
        xs[name] = max(ratios.values(), default=0)
    return header, xs, failures


def families(xs):
    """Group {cell name: x} by family, in cell order."""
    out = defaultdict(dict)
    for name, x in xs.items():
        out[name.split("/")[0]][name] = x
    return out


def verify(path, pinned=PINNED, family=None):
    """Return (summary lines, failures) for a transcript of the board, or of
    one family when family is set."""
    header, xs, failures = measure(path, pinned, family)
    lines = [f"seed={header['seed']} cells={len(xs)}", "family    cells  aggregate x_vs_best"]
    failures += [f"{n}: x_vs_best={x:.4f} is outside (0, {CELL_LIMIT}]"
                 for n, x in xs.items() if x is not None and not 0 < x <= CELL_LIMIT]
    for name, cells in families(xs).items():
        values = list(cells.values())
        if None in values:
            failures.append(f"{name}: {values.count(None)} of {len(values)} cells unanswered by casei")
            lines.append(f"{name:<9} {len(values):>5}  unanswered")
            continue
        aggregate = sum(values) / len(values)
        lines.append(f"{name:<9} {len(values):>5}  {aggregate:.4f}")
        if aggregate >= AGGREGATE_LIMIT:
            failures.append(f"{name}: aggregate x_vs_best={aggregate:.4f} is not below {AGGREGATE_LIMIT}")
    return lines, failures


def t_quantile(p, df):
    """Return Student's t quantile at probability p > 0.5 with df degrees of
    freedom: bisection on the CDF, integrating the density by Simpson's rule."""
    c = math.exp(math.lgamma((df + 1) / 2) - math.lgamma(df / 2)) / math.sqrt(df * math.pi)
    density = lambda x: c * (1 + x * x / df) ** (-(df + 1) / 2)

    def cdf(t, steps=200):
        h = t / steps
        inner = sum((4 if i % 2 else 2) * density(i * h) for i in range(1, steps))
        return 0.5 + h / 3 * (density(0) + inner + density(t))

    lo, hi = 0.0, 1.0
    while cdf(hi) < p:
        lo, hi = hi, 2 * hi
    for _ in range(50):
        mid = (lo + hi) / 2
        lo, hi = (mid, hi) if cdf(mid) < p else (lo, mid)
    return hi


def shift(master, change, alpha):
    """Return (delta, bound) for one family: delta is the change's aggregate
    x_vs_best minus master's, each averaged over its runs, and bound is the
    one-sided 1-alpha noise bound on delta. master and change hold at least two
    runs each, every run a list of the family's x_vs_best in one cell order.

    The noise is each cell's run-to-run variance within master and within the
    change: a real effect moves a cell's runs together, so it cannot widen the
    bound, however unevenly it spreads over the family. Cells are timed on
    their own, each against its field in alternating windows, so their noise
    adds independently; Welch-Satterthwaite gives the degrees of freedom."""
    n = len(master[0])
    parts = [(variance(cell) / len(runs) / n**2, len(runs) - 1) for runs in (master, change) for cell in zip(*runs)]
    se2 = sum(v for v, _ in parts)
    delta = fmean(map(fmean, change)) - fmean(map(fmean, master))
    if se2 == 0:
        return delta, 0.0
    df = se2**2 / sum(v * v / d for v, d in parts)
    return delta, t_quantile(1 - alpha, df) * math.sqrt(se2)


def compare(masters, changes, pinned=PINNED, family=None):
    """Return (summary lines, failures) for board runs of master and of a
    change on one host, or of one family when family is set. A family fails
    when its aggregate x_vs_best rose by more than its noise bound. The
    families share ALPHA, so each family's bound is at 1 - ALPHA/families."""
    if len(masters) < 2 or len(changes) < 2:
        raise VerificationError("compare needs at least two master runs and two change runs")
    runs, failures = {}, []
    for path in (*masters, *changes):
        _, xs, invalid = measure(path, pinned, family)
        runs[path] = families(xs)
        failures += [f"{path}: {f}" for f in invalid]
    names = sorted(set().union(*runs.values()))
    alpha = ALPHA / len(names)
    lines = [f"master runs={len(masters)} change runs={len(changes)} families={len(names)}",
             "family    cells  master  change   delta   bound  verdict"]
    for name in names:
        cells = [runs[p].get(name, {}) for p in runs]
        if any(set(c) != set(cells[0]) or None in c.values() for c in cells):
            failures.append(f"{name}: the runs do not all answer the same cells")
            continue
        order = sorted(cells[0])
        master = [[runs[p][name][n] for n in order] for p in masters]
        change = [[runs[p][name][n] for n in order] for p in changes]
        delta, bound = shift(master, change, alpha)
        base = fmean(map(fmean, master))
        verdict = "slower" if delta > bound else "faster" if delta < -bound else "same"
        lines.append(f"{name:<9} {len(order):>5}  {base:.4f}  {base + delta:.4f}  {delta:+.4f}  {bound:.4f}  {verdict}")
        if verdict == "slower":
            failures.append(f"{name}: aggregate x_vs_best rose by {delta:.4f}, above its noise bound {bound:.4f}")
    return lines, failures


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("transcript", type=Path, nargs="?")
    parser.add_argument("--family", help="check one family's runs instead of the whole board")
    parser.add_argument("--master", type=Path, nargs="+", default=[], help="compare: master's runs")
    parser.add_argument("--change", type=Path, nargs="+", default=[], help="compare: the change's runs")
    args = parser.parse_args()
    if (args.transcript is None) == (not args.master and not args.change):
        parser.error("give one transcript, or --master and --change runs")
    try:
        if args.transcript is None:
            lines, failures = compare(args.master, args.change, family=args.family)
        else:
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
