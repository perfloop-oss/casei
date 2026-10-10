#!/usr/bin/env python3

from pathlib import Path
import sys
import tempfile
import unittest

sys.dont_write_bytecode = True
import verify_board as verify


SEED, NAMES = verify.load_pinned()
HEADER = f"board: seed={SEED:#x} cells=200 vendor=GenuineIntel family=6 model=143 avx512f=1 avx512bw=1 avx512vbmi=1\n"


def ascii_tier(name):
    return int("script=ascii" in name and "corpus=russian" not in name)


def line(name, ratio=0.5, supported=1, drop=None, extra=None, x=None, wrong=None):
    cell = {"ascii_tier": ascii_tier(name)}
    active = verify.supported(name, cell) - {drop, wrong} | ({extra} - {None})
    if not supported:
        active = set()
    metrics = " ".join(
        f"{int(e in active)} {e}_active {ratio if e in active else 0} {e}_x {int(e == wrong)} {e}_wrong"
        for e in verify.ENTRANTS
    )
    reported = 0 if not supported else ratio if x is None else x
    return (f"BenchmarkBoard/{name}-8 1 1000 ns/op {reported} x_vs_best {supported} candidate_supported "
            f"{cell['ascii_tier']} ascii_tier 10 matches {metrics}\n")


def transcript(header=HEADER, skip=(), **per_cell):
    return header + "".join(line(n, **per_cell.get(n, {})) for n in NAMES if n not in skip)


def family(name):
    return [n for n in NAMES if n.startswith(name + "/")]


CS = family("case/cs")


class VerifyBoardTest(unittest.TestCase):
    def failures(self, text):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "board.txt"
            path.write_text(text)
            return verify.verify(path)[1]

    def assertFails(self, text, fragment):
        failures = self.failures(text)
        self.assertTrue(any(fragment in f for f in failures), failures)

    def test_accepts_a_winning_board(self):
        self.assertEqual(len(NAMES), 200)
        self.assertEqual(self.failures(transcript()), [])

    # A broken aggregate: every cell within 1.10, the family's mean at 1.0.
    def test_rejects_family_aggregate_at_one(self):
        cells = {n: {"ratio": 1.05 if i % 2 else 0.95} for i, n in enumerate(family("corpus"))}
        self.assertFails(transcript(**cells), "corpus: aggregate x_vs_best=1.0000")

    def test_aggregate_is_the_equal_weight_mean(self):
        cells = {n: {"ratio": 1.08 if i < 6 else 0.9} for i, n in enumerate(family("density"))}
        self.assertEqual(self.failures(transcript(**cells)), [])  # mean 0.945

    def test_rejects_x_vs_best_that_hides_a_faster_entrant(self):
        self.assertFails(transcript(**{family("op")[0]: {"x": 0.4}}), "is not the largest entrant ratio")

    def test_rejects_cell_above_limit(self):
        self.assertFails(transcript(**{family("size")[0]: {"ratio": 1.11}}), "above the 1.1 cell limit")

    def test_rejects_dropped_entrant(self):
        self.assertFails(transcript(**{family("count")[3]: {"drop": "rure"}}), "rure_active=0, want 1")

    def test_rejects_entrant_outside_its_tier(self):
        name = next(n for n in NAMES if not ascii_tier(n))
        self.assertFails(transcript(**{name: {"extra": "rustac"}}), "rustac_active=1, want 0")

    def test_rejects_wrong_entrant(self):
        name = family("script")[13]
        self.assertFails(transcript(**{name: {"wrong": "vectorscan"}}), f"{name}: vectorscan answered wrongly")

    def test_rejects_unsupported_case_sensitive_cells(self):
        failures = self.failures(transcript(**{n: {"supported": 0} for n in CS}))
        self.assertIn(f"{CS[0]}: casei does not support this cell", failures)
        self.assertIn("case: 12 of 24 cells unanswered by casei", failures)

    def test_rejects_hidden_cells(self):
        self.assertFails(transcript(skip=set(CS)), "cells differ from cells.txt")

    def test_rejects_unpinned_seed(self):
        self.assertFails(transcript(header=HEADER.replace(f"{SEED:#x}", "0x1")), "is not the pinned seed")

    def test_rejects_wrong_host(self):
        self.assertFails(transcript(header=HEADER.replace("model=143", "model=106")), "host: model=106")


if __name__ == "__main__":
    unittest.main()
