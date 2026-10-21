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


SEED, PINNED_CELLS = verify.load_pinned()
NAMES = sorted(PINNED_CELLS)
TIERS = {name: tier for name, (tier, _) in PINNED_CELLS.items()}
DIGESTS = {name: digest for name, (_, digest) in PINNED_CELLS.items()}
HEADER = f"board: seed={SEED:#x} cells=200 vendor=GenuineIntel family=6 model=143 avx512f=1 avx512bw=1 avx512vbmi=1\n"


def line(name, ratio=0.5, supported=1, drop=None, extra=None, faster=None):
    """A cell where every supporting entrant has ratio, except faster's 2x."""
    active = verify.supported(name, TIERS[name], DIGESTS[name]) - {drop} | ({extra} - {None})
    if not supported:
        active = set()
    metrics = " ".join(
        f"{int(e in active)} {e}_active {(2 * ratio if e == faster else ratio) if e in active else 0} {e}_x"
        for e in verify.ENTRANTS
    )
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
        name = next(n for n in NAMES if TIERS[n] == "utf8" and n.endswith(",case=ci")
                    and n not in verify.CERTIFIED_ADDITIONS)
        self.assertFails(transcript(**{name: {"extra": "rustac"}}), "rustac_active=1, want 0")
        name = next(n for n in NAMES if TIERS[n] == "ascii" and n.endswith(",case=ci"))
        self.assertFails(transcript(**{name: {"drop": "rustac"}}), "rustac_active=0, want 1")

    def test_certificates_bind_exact_names_digests_and_complete_roster(self):
        self.assertEqual(len(NAMES), 200)
        self.assertEqual(len(verify.CERTIFIED_ADDITIONS), 7)
        self.assertEqual(sum(len(entrants) for _, entrants in verify.CERTIFIED_ADDITIONS.values()), 13)
        for name, (digest, entrants) in verify.CERTIFIED_ADDITIONS.items():
            self.assertEqual(PINNED_CELLS[name], ("utf8", digest))
            self.assertEqual(verify.supported(name, TIERS[name], DIGESTS[name]) & {"rustac", "veloz"}, set(entrants))
            self.assertFalse(verify.supported(name, TIERS[name], "0" * 64) & {"rustac", "veloz"})

        folded = [name for name in NAMES if name.endswith(",case=ci")]
        exact = [name for name in NAMES if name.endswith(",case=cs")]
        self.assertEqual(len(folded), 112)
        self.assertEqual(len(exact), 88)
        old_folded = sum(len(verify.supported(name, TIERS[name], "0" * 64)) for name in folded)
        new_folded = sum(len(verify.supported(name, TIERS[name], DIGESTS[name])) for name in folded)
        old_all = sum(len(verify.supported(name, TIERS[name], "0" * 64)) for name in NAMES)
        new_all = sum(len(verify.supported(name, TIERS[name], DIGESTS[name])) for name in NAMES)
        self.assertEqual((old_folded, new_folded), (604, 617))
        self.assertEqual((old_all, new_all), (1132, 1145))
        family_sizes = {family: sum(name.split("/")[0] == family for name in NAMES)
                        for family in {name.split("/")[0] for name in NAMES}}
        self.assertEqual(family_sizes, {
            "count": 28, "length": 28, "script": 24, "case": 24,
            "corpus": 24, "density": 24, "size": 24, "op": 24,
        })

    def test_verifier_rejects_changed_certified_digest(self):
        name, (digest, _) = next(iter(verify.CERTIFIED_ADDITIONS.items()))
        with tempfile.TemporaryDirectory() as directory:
            pinned = Path(directory) / "cells.txt"
            rows = verify.PINNED.read_text().splitlines()
            changed = []
            for row in rows:
                if row.startswith(name + " "):
                    fields = row.split()
                    self.assertEqual(fields[2], digest)
                    fields[2] = "0" * 64
                    row = " ".join(fields)
                changed.append(row)
            pinned.write_text("\n".join(changed) + "\n")
            transcript_path = Path(directory) / "board.txt"
            transcript_path.write_text(transcript())
            _, _, failures = verify.measure(transcript_path, pinned=pinned)
        self.assertTrue(any(f"{name}: certified cell identity or digest changed" in f for f in failures), failures)

    def test_every_certified_slot_is_required(self):
        for name, (_, entrants) in verify.CERTIFIED_ADDITIONS.items():
            for entrant in entrants:
                with self.subTest(cell=name, entrant=entrant):
                    self.assertFails(transcript(**{name: {"drop": entrant}}),
                                     f"{name}: {entrant}_active=0, want 1")

    def test_every_certified_ratio_must_be_positive_finite(self):
        valid = transcript()
        for name, (_, entrants) in verify.CERTIFIED_ADDITIONS.items():
            original = line(name)
            for entrant in entrants:
                with self.subTest(cell=name, entrant=entrant):
                    corrupted = original.replace(f"0.5 {entrant}_x", f"nan {entrant}_x")
                    self.assertNotEqual(corrupted, original)
                    self.assertFails(valid.replace(original, corrupted),
                                     f"{name}: {entrant}_x=nan is not a positive finite ratio")

    def test_added_entrant_participates_in_largest_cell_ratio(self):
        name = next(iter(verify.CERTIFIED_ADDITIONS))
        self.assertFails(transcript(**{name: {"ratio": 0.6, "faster": "rustac"}}),
                         f"{name}: x_vs_best=1.2000 is outside (0, 1.1]")

    def test_unsupported_veloz_extra_on_multiliteral_cell_fails(self):
        name = next(n for n in NAMES if n in verify.CERTIFIED_ADDITIONS and "veloz" not in verify.CERTIFIED_ADDITIONS[n][1])
        self.assertFails(transcript(**{name: {"extra": "veloz"}}),
                         f"{name}: veloz_active=1, want 0")

    def test_case_sensitive_cells_need_the_exact_field(self):
        ascii = next(n for n in family("case/cs") if TIERS[n] == "ascii")
        utf8 = next(n for n in family("case/cs") if TIERS[n] == "utf8")
        self.assertEqual(verify.supported(utf8, "utf8", DIGESTS[utf8]), set(verify.ENTRANTS) - {"veloz"})
        self.assertFails(transcript(**{utf8: {"drop": "rustac"}}), f"{utf8}: rustac_active=0, want 1")
        self.assertFails(transcript(**{ascii: {"drop": "stringzilla"}}), f"{ascii}: stringzilla_active=0, want 1")
        # veloz has no exact mode, even on the one-pattern ASCII cells it races
        # when folding.
        single = next(n for n in NAMES if "/n=1," in n and n.endswith(",case=cs") and TIERS[n] == "ascii")
        self.assertEqual(verify.supported(single, "ascii", DIGESTS[single]), set(verify.ENTRANTS) - {"veloz"})
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
