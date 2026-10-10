#!/usr/bin/env python3

from pathlib import Path
import sys
import tempfile
import unittest

sys.dont_write_bytecode = True
import verify_board as verify


SEED, TIERS = verify.load_pinned()
NAMES = sorted(TIERS)
HEADER = f"board: seed={SEED:#x} cells=200 vendor=GenuineIntel family=6 model=143 avx512f=1 avx512bw=1 avx512vbmi=1\n"


def line(name, ratio=0.5, supported=1, drop=None, extra=None, faster=None):
    """A cell where every supporting entrant has ratio, except faster's 2x."""
    active = verify.supported(name, TIERS[name]) - {drop} | ({extra} - {None})
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

    def test_rejects_cell_above_limit(self):
        self.assertFails(transcript(**{family("size")[0]: {"ratio": 1.11}}), "is outside (0, 1.1]")

    def test_rejects_dropped_entrant(self):
        self.assertFails(transcript(**{family("count")[3]: {"drop": "rure"}}), "rure_active=0, want 1")

    def test_tier_comes_from_cells_txt(self):
        name = next(n for n in NAMES if TIERS[n] == "utf8")
        self.assertFails(transcript(**{name: {"extra": "rustac"}}), "rustac_active=1, want 0")
        name = next(n for n in NAMES if TIERS[n] == "ascii")
        self.assertFails(transcript(**{name: {"drop": "rustac"}}), "rustac_active=0, want 1")

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


if __name__ == "__main__":
    unittest.main()
