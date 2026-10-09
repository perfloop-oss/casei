#!/usr/bin/env python3

from pathlib import Path
import sys
import tempfile
import unittest

sys.dont_write_bytecode = True
import verify_board as verify


SEED, NAMES = verify.load_pinned()
HEADER = (
    f"board: seed={SEED:#x} cells=200 vendor=GenuineIntel family=6 model=143 "
    "avx512f=1 avx512bw=1 avx512vbmi=1 casei_vector_bits=512\n"
)


def attrs_of(name):
    return dict(item.split("=", 1) for item in name.rsplit("/", 1)[1].split(","))


def ascii_tier_of(name):
    a = attrs_of(name)
    return int(a["script"] == "ascii" and a["corpus"] != "russian")


def line(name, ratio=0.5, supported=None, drop=None, extra=None, x=None, wrong=None, ascii_tier=None, rename=None):
    a = attrs_of(name)
    if supported is None:
        supported = 1  # a winning board: casei answers every cell, cs included
    if ascii_tier is None:
        ascii_tier = ascii_tier_of(name)
    active = {"regexp", "pcre2", "rure", "vectorscan", "stringzilla"}
    if ascii_tier:
        active.add("rustac")
        if a["n"] == "1":
            active.add("veloz")
    for gone in (drop, wrong):
        active.discard(gone)
    if extra:
        active.add(extra)
    if not supported:
        active = set()
    metrics = []
    for e in verify.ENTRANTS:
        on = e in active
        metrics.append(f"{int(on)} {e}_active {ratio if on else 0} {e}_x {int(e == wrong)} {e}_wrong")
    competitors = len(active)
    reported = ratio if x is None else x
    if not supported:
        reported = 0
    shown = rename(name) if rename else name
    return (
        f"BenchmarkBoard/{shown}-8 1 1000 ns/op "
        f"{reported} x_vs_best {competitors} competitors {competitors + 1 if supported else 0} entrants "
        f"{supported} candidate_supported {ascii_tier} ascii_tier 10 matches 70000 bytes "
        + " ".join(metrics) + "\n"
    )


def key(name):
    return name.rsplit("/", 1)[0]


def transcript(header=HEADER, skip=(), **per_key):
    lines = [header] if header else []
    for name in NAMES:
        if key(name) in skip:
            continue
        lines.append(line(name, **per_key.get(key(name), {})))
    return "".join(lines)


def names_where(pred):
    return [n for n in NAMES if pred(n)]


def family_keys(family):
    return [key(n) for n in NAMES if n.startswith(family + "/")]


CS_KEYS = [key(n) for n in NAMES if n.startswith("case/cs/")]


class VerifyBoardTest(unittest.TestCase):
    def run_verify(self, text):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "board.txt"
            path.write_text(text)
            return verify.verify(path)

    def assertFails(self, text, fragment):
        _, failures = self.run_verify(text)
        self.assertTrue(any(fragment in f for f in failures), failures)

    def test_pinned_board_has_the_families(self):
        self.assertEqual(len(NAMES), 200)
        self.assertEqual({key(n) for n in NAMES}, verify.expected_keys())
        self.assertEqual(len(CS_KEYS), 12)

    def test_accepts_a_winning_board(self):
        lines, failures = self.run_verify(transcript())
        self.assertEqual(failures, [])
        self.assertIn(f"seed={SEED:#x} cells=200", lines[0])

    # A broken aggregate: each cell is within the 1.10 limit, but the family's
    # equal-weight total is not below the fastest entrant's.
    def test_rejects_family_aggregate_at_one(self):
        keys = family_keys("corpus")
        per_key = {k: {"ratio": 1.05 if i % 2 else 0.95} for i, k in enumerate(keys)}
        self.assertFails(transcript(**per_key), "corpus: aggregate x_vs_best=1.0000")

    def test_aggregate_is_the_equal_weight_mean(self):
        keys = family_keys("density")
        per_key = {k: {"ratio": 1.08 if i < 6 else 0.9} for i, k in enumerate(keys)}
        lines, failures = self.run_verify(transcript(**per_key))
        self.assertEqual(failures, [])
        self.assertTrue(any(l.startswith("density") and "0.9450" in l for l in lines), lines)

    def test_rejects_x_vs_best_that_hides_a_faster_entrant(self):
        self.assertFails(transcript(**{family_keys("op")[0]: {"x": 0.4}}), "is not the largest entrant ratio")

    def test_rejects_cell_above_limit(self):
        self.assertFails(transcript(**{family_keys("size")[0]: {"ratio": 1.11}}), "above the 1.1 cell limit")

    def test_rejects_dropped_entrant(self):
        self.assertFails(transcript(**{family_keys("count")[3]: {"drop": "rure"}}), "rure dropped")

    def test_rejects_dropped_veloz_on_ascii_single_pattern(self):
        name = names_where(lambda n: attrs_of(n)["n"] == "1" and ascii_tier_of(n) and "case=ci" in n)[0]
        self.assertFails(transcript(**{key(name): {"drop": "veloz"}}), "veloz dropped")

    def test_rejects_rustac_on_utf8_tier(self):
        name = names_where(lambda n: not ascii_tier_of(n))[0]
        self.assertFails(transcript(**{key(name): {"extra": "rustac"}}), "rustac counted outside its tier")

    def test_lists_wrong_entrant_without_failing(self):
        k = family_keys("script")[13]
        lines, failures = self.run_verify(transcript(**{k: {"wrong": "vectorscan"}}))
        self.assertEqual(failures, [])
        self.assertTrue(any(l.startswith("vectorscan answered 1 cells wrongly") and k in l for l in lines), lines)

    def test_rejects_unsupported_case_sensitive_cell(self):
        _, failures = self.run_verify(transcript(**{k: {"supported": 0} for k in CS_KEYS}))
        self.assertTrue(any("casei does not support a case-sensitive cell" in f for f in failures))
        self.assertTrue(any(f.startswith("case: 12 of 24 cells unanswered") for f in failures), failures)

    def test_rejects_hidden_case_sensitive_cells(self):
        self.assertFails(transcript(skip=set(CS_KEYS)), "cell inventory differs")

    def test_rejects_case_sensitive_cell_relabeled_insensitive(self):
        relabel = lambda n: n.replace("case=cs", "case=ci")
        _, failures = self.run_verify(transcript(**{CS_KEYS[0]: {"rename": relabel}}))
        self.assertTrue(any("outside its level" in f for f in failures), failures)
        self.assertTrue(any("cell inventory differs" in f for f in failures), failures)

    def test_rejects_a_cell_that_is_not_pinned(self):
        bigger = lambda n: n.replace("/n=", "/n=9")
        self.assertFails(transcript(**{family_keys("op")[2]: {"rename": bigger}}), "cell inventory differs")

    def test_rejects_unpinned_seed(self):
        self.assertFails(transcript(header=HEADER.replace(f"seed={SEED:#x}", "seed=0x1")), "is not the pinned seed")

    def test_rejects_wrong_host(self):
        self.assertFails(transcript(header=HEADER.replace("model=143", "model=106")), "host: model=106")

    def test_rejects_missing_seed(self):
        with self.assertRaisesRegex(verify.VerificationError, "no 'board: seed"):
            self.run_verify(transcript(header=""))

    def test_rejects_duplicate_cell(self):
        with self.assertRaisesRegex(verify.VerificationError, "appears twice"):
            self.run_verify(transcript() + line(NAMES[0]))

    def test_rejects_two_runs(self):
        with self.assertRaisesRegex(verify.VerificationError, "more than one board run"):
            self.run_verify(HEADER + transcript())


if __name__ == "__main__":
    unittest.main()
