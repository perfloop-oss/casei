#!/usr/bin/env python3

from pathlib import Path
import random
import sys
import tempfile
import math
from collections import defaultdict
from statistics import fmean
import unittest

sys.dont_write_bytecode = True
import verify_board as verify


SEED, TIERS = verify.load_pinned()
NAMES = sorted(TIERS)
HEADER = f"board: seed={SEED:#x} cells=200 vendor=GenuineIntel family=6 model=143 avx2=1 avx512f=1 avx512bw=1 avx512vbmi=1\n"
CERTIFIED_RUSTAC_UTF8 = {
    "length/3-4/03/n=1,len=3-4,size=206100,first=0.93,spacing=1024,op=indexfold,nonascii=100,corpus=code,case=ci": "exact",
    "size/1MiB-4MiB/00/n=1,len=3-4,size=2451052,first=0.56,spacing=65536,op=indexfold,nonascii=100,corpus=code,case=ci": "exact",
    "case/ci/09/n=1,len=5-8,size=159739,first=0.26,spacing=0,op=indexfold,nonascii=0,corpus=russian,case=ci": "ascii",
    "corpus/russian/02/n=1,len=3-4,size=9564,first=0.63,spacing=65536,op=each,nonascii=50,corpus=russian,case=ci": "ascii",
    "op/indexfold/00/n=1,len=5-8,size=11181,first=0.11,spacing=1024,op=indexfold,nonascii=0,corpus=russian,case=ci": "ascii",
    "op/indexfold/04/n=1,len=2-2,size=22972,first=0.86,spacing=0,op=indexfold,nonascii=50,corpus=russian,case=ci": "ascii",
    "script/ascii/01/n=2,len=2-2,size=34069,first=1.00,spacing=65536,op=find,nonascii=0,corpus=russian,case=ci": "ascii",
}


def expected_supported(name, tier):
    """Independent fixture roster from the frozen board contract."""
    out = {"regexp", "pcre2", "rure", "vectorscan", "stringzilla"}
    if name.endswith(",case=cs"):
        return out | {"rustac"}
    return out | {"veloz", "rustac"}


def expected_mode(name, tier, active):
    if "veloz" not in active or name.endswith(",case=cs"):
        return 0, 0
    if tier == "ascii" and "/n=1," in name:
        return 1, 0
    return 0, 1


def expected_rustac_mode(name, tier, active):
    if "rustac" not in active:
        return 0, 0
    if name.endswith(",case=cs") or tier == "ascii" or name in CERTIFIED_RUSTAC_UTF8:
        return 1, 0
    return 0, 1


def line(name, ratio=0.5, supported=1, drop=None, extra=None, faster=None, mode=None, rustac_mode=None):
    """A cell where every supporting entrant has ratio, except faster's 2x."""
    active = expected_supported(name, TIERS[name]) - {drop} | ({extra} - {None})
    if not supported:
        active = set()
    direct, view = expected_mode(name, TIERS[name], active) if mode is None else mode
    rustac_direct, rustac_view = expected_rustac_mode(name, TIERS[name], active) if rustac_mode is None else rustac_mode
    metrics = " ".join(
        f"{int(e in active)} {e}_active {(2 * ratio if e == faster else ratio) if e in active else 0} {e}_x"
        for e in verify.ENTRANTS
    )
    metrics += f" {direct} veloz_direct_active {view} veloz_view_active"
    metrics += f" {rustac_direct} rustac_direct_active {rustac_view} rustac_view_active"
    return f"BenchmarkBoard/{name}-8 1 1000 ns/op {supported} candidate_supported {metrics}\n"


def transcript(header=HEADER, skip=(), only="", **per_cell):
    return header + "".join(line(n, **per_cell.get(n, {})) for n in NAMES if n not in skip and n.startswith(only))


def family(prefix):
    return [n for n in NAMES if n.startswith(prefix + "/")]


class VerifyBoardTest(unittest.TestCase):
    def verify(self, text, family=None):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "board.txt"
            path.write_text(text)
            return verify.verify(path, family=family)

    def assertFails(self, text, fragment, family=None):
        failures = self.verify(text, family)[1]
        self.assertTrue(any(fragment in f for f in failures), failures)

    def test_accepts_a_winning_board(self):
        self.assertEqual(len(NAMES), 200)
        self.assertEqual(self.verify(transcript())[1], [])

    def test_support_roster_covers_all_original_folded_cells(self):
        folded = [n for n in NAMES if not n.endswith(",case=cs")]
        exact = [n for n in NAMES if n.endswith(",case=cs")]
        self.assertEqual((len(folded), len(exact)), (112, 88))
        self.assertEqual({n.split(",op=")[1].split(",")[0] for n in folded}, {"find", "each", "indexfold"})
        for name in NAMES:
            self.assertEqual(verify.supported(name, TIERS[name]), expected_supported(name, TIERS[name]), name)

    def test_verifier_rejects_a_missing_veloz_cell_for_each_operation(self):
        for op in ("find", "each", "indexfold"):
            name = next(n for n in NAMES if f",op={op}," in n and not n.endswith(",case=cs"))
            self.assertFails(transcript(**{name: {"drop": "veloz"}}), f"{name}: veloz_active=0, want 1")

    def test_verifier_rejects_corrupt_veloz_ratios(self):
        name = next(n for n in NAMES if TIERS[n] == "utf8" and not n.endswith(",case=cs"))
        for bad in ("-0.5", "nan", "inf"):
            text = transcript().replace(line(name), line(name).replace("0.5 veloz_x", f"{bad} veloz_x"))
            self.assertFails(text, f"{name}: veloz_x={bad} is not a positive finite ratio")

    def test_verifier_rejects_wrong_veloz_modes(self):
        direct = next(n for n in NAMES if TIERS[n] == "ascii" and "/n=1," in n and not n.endswith(",case=cs"))
        view = next(n for n in NAMES if not n.endswith(",case=cs") and expected_mode(n, TIERS[n], expected_supported(n, TIERS[n])) == (0, 1))
        self.assertFails(transcript(**{direct: {"mode": (0, 1)}}), f"{direct}: veloz_direct_active=0, want 1")
        self.assertFails(transcript(**{view: {"mode": (1, 0)}}), f"{view}: veloz_view_active=0, want 1")

    # A broken aggregate: every cell within 1.10, the family's mean at 1.0.
    def test_rejects_family_aggregate_at_one(self):
        cells = {n: {"ratio": 1.05 if i % 2 else 0.95} for i, n in enumerate(family("corpus"))}
        self.assertFails(transcript(**cells), "corpus: aggregate x_vs_best=1.0000")

    def test_aggregate_is_the_equal_weight_mean(self):
        cells = {n: {"ratio": 1.08 if i < 6 else 0.9} for i, n in enumerate(family("density"))}
        lines, failures = self.verify(transcript(**cells))
        self.assertEqual(failures, [])
        self.assertIn("density      24  0.9450", lines)

    def test_x_vs_best_is_the_fastest_entrant(self):
        name = family("op")[0]
        self.assertFails(transcript(**{name: {"ratio": 0.6, "faster": "pcre2"}}), f"{name}: x_vs_best=1.2000")

    # A corrupt ratio fails even when another entrant sets a valid x_vs_best.
    def test_rejects_corrupt_entrant_ratio(self):
        name = family("op")[0]
        for bad in ("-0.5", "nan", "inf"):
            text = transcript().replace(line(name), line(name).replace("0.5 vectorscan_x", f"{bad} vectorscan_x"))
            self.assertFails(text, f"{name}: vectorscan_x={bad} is not a positive finite ratio")

    def test_rejects_cell_above_limit(self):
        self.assertFails(transcript(**{family("size")[0]: {"ratio": 1.11}}), "is outside (0, 1.1]")

    def test_rejects_dropped_entrant(self):
        self.assertFails(transcript(**{family("count")[3]: {"drop": "rure"}}), "rure_active=0, want 1")

    def test_tier_comes_from_cells_txt(self):
        utf8 = next(n for n in NAMES if TIERS[n] == "utf8" and n.endswith(",case=ci"))
        ascii = next(n for n in NAMES if TIERS[n] == "ascii" and n.endswith(",case=ci"))
        exact = next(n for n in NAMES if n.endswith(",case=cs") and TIERS[n] == "utf8")
        self.assertFails(transcript(**{utf8: {"drop": "rustac"}}), "rustac_active=0, want 1")
        self.assertFails(transcript(**{utf8: {"rustac_mode": (1, 0)}}), "rustac_view_active=0, want 1")
        self.assertFails(transcript(**{ascii: {"rustac_mode": (0, 1)}}), "rustac_view_active=1, want 0")
        self.assertFails(transcript(**{exact: {"rustac_mode": (0, 1)}}), "rustac_view_active=1, want 0")
        direct = next(n for n in NAMES if n in CERTIFIED_RUSTAC_UTF8)
        self.assertFails(transcript(**{direct: {"rustac_mode": (0, 1)}}), "rustac_direct_active=0, want 1")

    def test_rustac_supports_every_cell_and_pins_exactly_seven_folded_utf8_modes(self):
        self.assertEqual(sum("rustac" in verify.supported(n, tier) for n, tier in TIERS.items()), 200)
        folded_utf8 = {n for n, tier in TIERS.items() if tier == "utf8" and not n.endswith(",case=cs")}
        self.assertEqual(len(folded_utf8), 77)
        self.assertTrue(set(CERTIFIED_RUSTAC_UTF8) <= folded_utf8)
        self.assertTrue(all(TIERS[n] == "utf8" and n.endswith(",case=ci") for n in CERTIFIED_RUSTAC_UTF8))
        self.assertEqual(CERTIFIED_RUSTAC_UTF8, verify.RUSTAC_CERTIFIED_UTF8_MODES)
        self.assertEqual(list(CERTIFIED_RUSTAC_UTF8.values()).count("exact"), 2)
        self.assertEqual(list(CERTIFIED_RUSTAC_UTF8.values()).count("ascii"), 5)
        direct = {n for n in folded_utf8 if expected_rustac_mode(n, "utf8", {"rustac"}) == (1, 0)}
        views = {n for n in folded_utf8 if expected_rustac_mode(n, "utf8", {"rustac"}) == (0, 1)}
        self.assertEqual(direct, set(CERTIFIED_RUSTAC_UTF8))
        self.assertEqual(len(views), 70)
        mixed = next(n for n in folded_utf8 if n.startswith("count/2/01/"))
        self.assertEqual(expected_rustac_mode(mixed, "utf8", {"rustac"}), (0, 1))
        failures = self.verify(transcript(**{mixed: {"rustac_mode": (1, 0)}}))[1]
        self.assertTrue(any(f"{mixed}: rustac_view_active=0, want 1" in failure for failure in failures), failures)
        exact = next(n for n in NAMES if n.endswith(",case=cs") and TIERS[n] == "utf8")
        self.assertFails(transcript(**{exact: {"rustac_mode": (0, 1)}}), "rustac_view_active=1, want 0")

    def test_verifier_rejects_a_missing_rustac_cell_for_each_operation(self):
        for op in ("find", "each", "indexfold"):
            name = next(n for n in NAMES if f",op={op}," in n and not n.endswith(",case=cs"))
            self.assertFails(transcript(**{name: {"drop": "rustac"}}), f"{name}: rustac_active=0, want 1")

    def test_verifier_rejects_corrupt_rustac_ratios(self):
        name = next(n for n in NAMES if TIERS[n] == "utf8" and n.endswith(",case=ci"))
        for bad in ("-0.5", "nan", "inf"):
            text = transcript().replace(line(name), line(name).replace("0.5 rustac_x", f"{bad} rustac_x"))
            self.assertFails(text, f"{name}: rustac_x={bad} is not a positive finite ratio")

    def test_case_sensitive_cells_need_the_exact_field(self):
        ascii = next(n for n in family("case/cs") if TIERS[n] == "ascii")
        utf8 = next(n for n in family("case/cs") if TIERS[n] == "utf8")
        self.assertEqual(expected_supported(utf8, "utf8"), set(verify.ENTRANTS) - {"veloz"})
        self.assertFails(transcript(**{utf8: {"drop": "rustac"}}), f"{utf8}: rustac_active=0, want 1")
        self.assertFails(transcript(**{ascii: {"drop": "stringzilla"}}), f"{ascii}: stringzilla_active=0, want 1")
        # veloz has no exact mode, even on the one-pattern ASCII cells it races
        # when folding.
        single = next(n for n in NAMES if "/n=1," in n and n.endswith(",case=cs") and TIERS[n] == "ascii")
        self.assertEqual(expected_supported(single, "ascii"), set(verify.ENTRANTS) - {"veloz"})
        self.assertFails(transcript(**{single: {"extra": "veloz"}}), f"{single}: veloz_active=1, want 0")

    def test_rejects_unsupported_case_sensitive_cells(self):
        cs = family("case/cs")
        failures = self.verify(transcript(**{n: {"supported": 0} for n in cs}))[1]
        self.assertIn(f"{cs[0]}: casei does not support this cell", failures)
        self.assertIn("case: 12 of 24 cells unanswered by casei", failures)

    def test_rejects_hidden_cells(self):
        self.assertFails(transcript(skip=set(family("case/cs"))), "cells differ from cells.txt")

    def test_rejects_unpinned_seed(self):
        self.assertFails(transcript(header=HEADER.replace(f"{SEED:#x}", "0x1")), "is not the pinned seed")

    def test_rejects_wrong_host(self):
        self.assertFails(transcript(header=HEADER.replace("model=143", "model=106")), "host: model=106")
        self.assertFails(transcript(header=HEADER.replace("avx2=1", "avx2=0")), "host: avx2=0, want 1")


    def test_verifies_one_family(self):
        run = transcript(only="density/")
        self.assertEqual(self.verify(run, "density")[1], [])
        self.assertFails(run, "cells differ from cells.txt")  # not the board
        self.assertFails(transcript(only="density/", skip={family("density")[0]}), "cells differ", "density")
        cells = {n: {"ratio": 1.05 if i % 2 else 0.95} for i, n in enumerate(family("density"))}
        self.assertFails(transcript(only="density/", **cells), "density: aggregate", "density")
        self.assertFails(transcript(only="density/", **{family("density")[1]: {"drop": "pcre2"}}), "pcre2_active=0", "density")


    def test_family_must_be_a_pinned_family(self):
        run = transcript(only="density/")
        for bad in ("nosuch", "", "densit", "case/ci"):
            with self.assertRaisesRegex(verify.VerificationError, "is not a pinned family"):
                self.verify(run, bad)
        self.assertFails(HEADER, "the run has no cells")



def noisy(rng, sigma, slower=None, by=0.0, cells=()):
    """Per-cell ratios for one run: every cell 0.8 under multiplicative noise
    sigma, the cells of family slower by a factor 1+by, and cells x0.2."""
    def ratio(n):
        return 0.8 * (1 + by * n.startswith(f"{slower}/")) * (0.2 if n in cells else 1) * math.exp(rng.gauss(0, sigma))
    return {n: {"ratio": round(ratio(n), 6)} for n in NAMES}


FAMILIES = sorted({n.split("/")[0] for n in NAMES})


def board(rng, sigma, runs, slower=None, by=0.0):
    """{family: one list of cell ratios per run}, as compare passes to shift."""
    out = defaultdict(list)
    for _ in range(runs):
        run = noisy(rng, sigma, slower, by)
        for f in FAMILIES:
            out[f].append([run[n]["ratio"] for n in family(f)])
    return out


class CompareTest(unittest.TestCase):
    def compare(self, masters, changes, family=None):
        with tempfile.TemporaryDirectory() as directory:
            def write(kind, texts):
                paths = [Path(directory) / f"{kind}{i}.txt" for i in range(len(texts))]
                for path, text in zip(paths, texts):
                    path.write_text(text)
                return paths
            return verify.compare(write("master", masters), write("change", changes), family=family)

    def runs(self, seed, family=None, **change):
        """Two master and two change transcripts under 3% cell noise."""
        rng = random.Random(seed)
        only = f"{family}/" if family else ""
        return ([transcript(only=only, **noisy(rng, 0.03)) for _ in range(2)],
                [transcript(only=only, **noisy(rng, 0.03, **change)) for _ in range(2)])

    def verdicts(self, lines):
        return {l.split()[0]: l.split()[-1] for l in lines[2:]}

    def test_neutral_change_passes(self):
        lines, failures = self.compare(*self.runs(1))
        self.assertEqual(failures, [])
        self.assertEqual(set(self.verdicts(lines).values()), {"same"})

    def test_family_regression_fails(self):
        lines, failures = self.compare(*self.runs(2, slower="script", by=0.04))
        self.assertEqual([f.split(":")[0] for f in failures], ["script"], failures)
        self.assertEqual(self.verdicts(lines)["script"], "slower")

    # A real effect on a few cells does not widen the bound: the family is
    # named faster, not hidden in noise.
    def test_targeted_improvement_is_named(self):
        lines, failures = self.compare(*self.runs(3, cells=family("count")[:2]))
        self.assertEqual(failures, [])
        self.assertEqual(self.verdicts(lines)["count"], "faster")

    def test_compares_one_family(self):
        self.assertEqual(self.compare(*self.runs(6, "density"), family="density")[1], [])
        failures = self.compare(*self.runs(7, "density", slower="density", by=0.05), family="density")[1]
        self.assertEqual([f.split(":")[0] for f in failures], ["density"])

    def test_compare_checks_each_run(self):
        bad = transcript(header=HEADER.replace("model=143", "model=106"))
        failures = self.compare([transcript()] * 2, [transcript(), bad])[1]
        self.assertTrue(any("change1.txt: host: model=106" in f for f in failures), failures)
        unanswered = transcript(**{family("case")[0]: {"supported": 0}})
        self.assertIn("case: the runs do not all answer the same cells",
                      self.compare([transcript()] * 2, [transcript(), unanswered])[1])
        with self.assertRaisesRegex(verify.VerificationError, "at least two"):
            self.compare([transcript()], [transcript()])

    def test_shift_exact(self):
        # One cell, two runs each: variance 2 per arm over 2 runs gives se^2 = 2
        # and df = 2^2 / (1 + 1) = 2.
        delta, bound = verify.shift([[1], [3]], [[2], [4]], 0.05)
        self.assertEqual(delta, 1)
        self.assertAlmostEqual(bound, verify.t_quantile(0.95, 2) * math.sqrt(2))

    # A rise between one and two bounds is slower: the verdict is delta > bound.
    def test_rise_above_one_bound_is_slower(self):
        cells = family("op")
        spread = [0.01 * (1 + i % 5) for i in range(len(cells))]
        master = [[0.5 + s for s in spread], [0.5 - s for s in spread]]
        _, bound = verify.shift(master, master, verify.ALPHA / len(FAMILIES))
        change = [[x + 1.5 * bound for x in run] for run in master]
        text = lambda run: transcript(**{n: {"ratio": x} for n, x in zip(cells, run)})
        lines, failures = self.compare([text(r) for r in master], [text(r) for r in change])
        self.assertEqual(self.verdicts(lines)["op"], "slower", lines)
        self.assertEqual([f.split(":")[0] for f in failures], ["op"], failures)

    def test_t_quantile(self):
        for p, df, want in ((0.995, 2, 9.925), (0.975, 10, 2.228), (0.99, 23, 2.500), (0.995, 27, 2.771)):
            self.assertAlmostEqual(verify.t_quantile(p, df), want, places=3)

    # With no real effect, the rule this guard replaces -- no family aggregate
    # above master's by more than the spread |A1-A2| of two master runs --
    # fails most changes, at any noise level. Against the mean of the master
    # runs, P(C - mean > |A1-A2|) = atan(sqrt(3/4))/pi = 0.227 per family
    # under normal noise, so 1 - 0.773**8 = 0.87 across 8 families; against
    # the larger master run, 0.17 and 0.77. compare fails at most ALPHA of
    # them, and still fails a 4% family regression.
    def test_false_rejects_and_power(self):
        rng, trials, sigma, alpha = random.Random(8), 300, 0.03, verify.ALPHA / len(FAMILIES)
        slower = lambda m, c: (lambda d, bound: d > bound)(*verify.shift(m, c, alpha))
        old = new = caught = 0
        for _ in range(trials):
            m, c = board(rng, sigma, 2), board(rng, sigma, 2)
            a1, a2, change = (lambda f: fmean(m[f][0])), (lambda f: fmean(m[f][1])), (lambda f: fmean(c[f][0]))
            old += any(change(f) - (a1(f) + a2(f)) / 2 > abs(a1(f) - a2(f)) for f in FAMILIES)
            new += any(slower(m[f], c[f]) for f in FAMILIES)
            caught += slower(m["script"], board(rng, sigma, 2, "script", 0.04)["script"])
        self.assertGreater(old / trials, 0.8)
        self.assertLess(new / trials, 0.05)
        self.assertGreater(caught / trials, 0.95)

if __name__ == "__main__":
    unittest.main()
