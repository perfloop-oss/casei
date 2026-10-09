# How casei works

## Ten seconds

`casei` does cheap byte tests before it pays for Unicode.

It compiles a pattern set into one exact search plan and a set of conservative
byte filters. The filters reject impossible starts in blocks. The exact plan
checks every survivor.

```text
                         exact fold-token plan
                       /                       \
patterns -> compile once                         -> leftmost correct match
                       \                       /
                         conservative byte sieve
```

The sieve may say "maybe" too often. It may never skip a real match. That one
rule lets the fast path use compact byte tables while the plan keeps complete
Unicode semantics.

## One minute

Suppose the text is one megabyte long and the pattern is `fatal panic`.
Checking every byte as a possible start repeats almost the same failure over
and over.

At compile time, `casei` chooses byte positions that are useful for this exact
literal. On AVX-512, one comparison covers 64 possible starts. A few comparison
masks are intersected:

```text
candidate start       0 1 2 3 4 5 ... 63
probe at offset A     0 0 1 0 0 0 ...  0
probe at offset B     0 0 1 0 1 0 ...  0
probe at offset C     0 0 0 0 1 0 ...  0
                      -------------------- AND
survivors             0 0 0 0 0 0 ...  0
```

No survivor means the whole block is done. A survivor is replayed through the
exact plan. That plan decides the match, source byte offset, leftmost order,
and pattern-ID tie.

The compiler can choose adjacent pairs, dispersed bytes, triples, Shufti-style
tables, pair-pair filters, or tagged multi-pattern anchors. The choice comes
from the compiled pattern shape. It never comes from the benchmark name.

## Why the advantage exists

The advantage has three layers.

### 1. A smaller question

The input is a literal or finite set of literals under Unicode simple folding.
The answer is the first leftmost match, or a stream of non-overlapping matches.

A regex engine must preserve classes, captures, repetition, lookarounds, and
the rest of its language. `casei` can spend compilation on literal-specific
byte positions and a literal-specific confirmation plan. Arena adapters make
every entrant answer the same result contract and charge any adaptation to the
entrant being adapted.

### 2. Less work

Most bytes never reach the Unicode executor. A block filter proves that no
match can start at any of 64 positions. Multi-pattern filters also return an
eight-bit pattern tag, so the exact replay checks only the literals named by
the surviving lane.

The focused paths add more ways to avoid unnecessary replay:

- A single Unicode literal can compile its one-, two-, and three-byte fold
  spellings into a bounded raw confirmation. The confirmer advances by the
  spelling that matched and returns the source width it proved.
- An eligible multi-pattern plan can choose an exact ASCII byte present in
  every spelling of every literal. The first occurrence bounds where a match
  could begin. A dedicated kernel scans eight 64-byte blocks before it tests
  the ordered masks.

Long ASCII literals with width-changing fold mates use the same split on
mostly ASCII input. The block probe searches each clean gap. A narrow decoded
halo owns a sparse Unicode cluster and the starts that can cross its boundary.
The probe then resumes on the next clean gap.

```text
source      [ clean ASCII ][ Unicode cluster ][ clean ASCII ]
executor      block probe      exact halo        block probe
```

These are gates into the same plan. Search order, byte offsets, and source
widths remain with the exact owner.

The [two-host ablations](audit/acceptance/ablations/README.md) remove the origin
gate, variable confirmation, and returned pattern tags one at a time. Each
removal loses a required focused field or Rebar row on at least one host.

### 3. Hardware-shaped kernels

AVX-512 BW compares 64 bytes at a time. VBMI performs byte-table lookup in a
register. Mask registers hold candidate sets without converting them back to
ordinary vectors.

Instruction latency still matters. On Ice Lake, OpcodeX reports three-cycle
latency and one-per-cycle reciprocal throughput for the unmasked ZMM `VPERMB`
used by the hot table loops. A loop that starts one block and waits leaves issue
slots unused. The hand-written kernels carry independent blocks through those
three cycles.

The exact common-byte gate is a simple example:

```text
load+compare block 0 -> k0
load+compare block 1 -> k1
...
load+compare block 7 -> k7
ordered pair tests find the first non-empty mask
```

OpcodeX reports the memory-source `VPCMPEQB` form at three-cycle latency and
one-per-cycle throughput on Ice Lake. Its current uops.info catalog has no
Sapphire Rapids column, so the instruction table is used only to explain the
Ice Lake schedule. Eight independent compares give the core work while earlier
masks are in flight. The Sapphire Rapids result comes from direct execution and
the complete field measurements.

The tagged multi-anchor kernel uses the same idea with `VPERMB` tables. Its
common sparse path proves eight blocks empty. A hit returns to a four-block
dispatcher, preserves byte order, extracts the first lane's tag byte, and lets
Go perform exact pair and plan checks only for those tags.

<details>
<summary>Unicode coverage and the shared multi-pattern plan</summary>

## Unicode without hand-waving

Lowercasing both strings is a different operation. Simple folding groups runes
into orbits:

```text
k    K    K       one byte, one byte, three bytes
s    S    ſ       one byte, one byte, two bytes
σ    ς    Σ       three runes in one orbit
```

`casei` compiles each orbit to one token. Valid haystack runes map to those
tokens. Invalid UTF-8 bytes map to opaque one-byte tokens. The shared state
machine advances over tokens and owns the answer.

Raw filters are derived from that plan under coverage proofs:

- Fixed offsets are used only while every earlier fold spelling has the same
  width.
- Pair and triple tables contain every possible raw spelling for the token
  window they represent.
- A low-six-bit `VPERMB` alias may create a false survivor. Exact replay removes
  it.
- Variable confirmation stores complete raw forms and advances by the form
  that matched.
- A filter route is disabled when the compiler cannot prove coverage.

This makes false positives a performance cost. False negatives are a
correctness bug.

## One pattern and many patterns

`NewMatcher` compiles the whole set into a trie with failure transitions over
fold tokens. Small plans use sparse edges. Larger plans can materialize dense
transitions.

```text
N=1      one plan, one traversal
N=8      one shared plan, tagged filters, one traversal
N=512    one shared plan, shared filters, one traversal
```

Terminals carry the original pattern ID. The plan waits only long enough to
prove the leftmost source byte. Equal starts choose the lowest ID.

The tagged Unicode route is important for enumeration. Each literal contributes
selective interior pairs and their possible source-start offsets. The vector
screen intersects primary and confirmation pairs while carrying the owning
pattern bits. Exact pair checks remove table aliases. The existing raw token
plan then proves the match and its width.

</details>

<details>
<summary>Competitor implementations and the assembly A/B</summary>

## What the competitors do

Every scoring entrant is open source. The audit read the level where its hot
answer lives: source, intrinsics, generated assembly, or built-object
disassembly.

| entrant | strong path in this field | where casei differs |
|---|---|---|
| Vectorscan 5.4.12 | Mature Teddy, FDR, and vermicelli machinery with a 512-bit VBMI database. The inspected Sapphire Rapids hot miss path entered `avx512vbmi_vermicelliExec`. | `casei` compiles only the simple-fold literal-set question and joins its filter to the same plan that owns leftmost and pattern ties. The arena compares both at 512 bits. |
| PCRE2 10.47 JIT | Native JIT with strong prefix analysis for a general regex language. It reported a 128-bit path here. | `casei` can select several literal-specific byte positions and raw fold spellings without preserving general regex behavior. |
| rust/regex | Automata plus memchr and Teddy prefilters. Eligible observed paths reported AVX2. | `casei` shares one fold-token plan across the literal set and uses AVX-512 masks and VBMI tables around it. |
| StringZilla 5.1.2 | Dedicated SIMD Unicode search with full-fold expansions. | Full folding is a different relation. The arena charges the verification needed to reduce its candidates to simple-fold results. |
| veloz | Hand-written AVX2 search for one ASCII literal. | `casei` answers Unicode and multi-pattern questions and uses 512-bit kernels on the measured hosts. |
| Rust Aho-Corasick | Strong ASCII multi-pattern DFA with a Teddy prefilter. | `casei` extends the shared-plan shape to simple-fold orbits, variable UTF-8 widths, opaque invalid bytes, and AVX-512 filters. |
| Go regexp | Correct simple folding through the standard scalar regexp engine. | It is the semantic floor. `casei` is a compiled literal engine. |

Shufti, Teddy, rare anchors, tries, failure links, byte-table lookup, and
confirmation after a candidate are known techniques. The result comes from
combining them under this contract and driving the combination to a field
position no entrant held. [`NOVELTY.md`](NOVELTY.md) makes that claim and its
falsifiers explicit.

## Does the assembly matter?

Yes. It was measured.

The complete amd64 backend was re-expressed with Go's experimental
`simd/archsimd` package. It passed direct tests and differential fuzzing. On
the required Ice Lake `single/log_miss_1mb` row, six alternating-order runs put
the generated backend at 20.8 to 23.3 µs/op and `0.878` to `0.913 x_vs_best`.
The assembly control measured 18.4 to 20.8 µs/op and `0.785` to `0.797`.

The disassembly explains the gap. The generated hot loop handles one block,
materializes a vector intersection, converts it to a mask, and crosses that
mask to a general register on each iteration. The assembly loop interleaves
four independent blocks and produces k-masks directly with `VPTESTMB`. Larger
generated Shufti functions also spill wide tables to 144-byte and 512-byte
stack frames. Their assembly controls keep the tables in vector registers.

That experiment is public in the
[Go SIMD backend Case](https://app.perfloop.ai/t/oss/case_37sjyc8f94).

The assembly audit also killed changes that looked attractive on paper:

- Combining k-masks with seven extra `KORQ` instructions did not reduce the
  tested dependency cost.
- `PREFETCHT0` at 512, 1024, 2048, and 4096 bytes was neutral or slower in the
  sparse tagged loop.
- Boolean fusion, target alignment, and a BITALG scout failed their paired
  controls.

Removed experiments and their falsifiers live in [`NOVELTY.md`](NOVELTY.md).

</details>

<details>
<summary>Verification, limits, and code map</summary>

## How the result is checked

1. Deterministic differentials compare single and multi results with Go
   `regexp (?i)` on valid UTF-8 and an opaque-byte oracle on invalid input.
2. `FuzzIndexFold` and `FuzzMatcher` run those same contracts. The current
   publication source was fuzzed on the portable ARM path and both AVX-512
   hosts.
3. Direct assembly models cover randomized lengths, every 64-byte boundary,
   tails, dense and sparse epochs, false table aliases, and exact source widths.
4. GDB breakpoints on both CPUs proved that the three new native kernels were
   reached by their direct tests.
5. Every field entrant is rebuilt from pinned source and checked for actual
   dispatch before timing.
6. Three complete paired passes require all 38 arena rows below 1.0 on Ice Lake
   and Sapphire Rapids.
7. The checked-in Rebar receipt requires all five same-contract external rows
   below 1.0 on both CPUs.
8. The active publication target extends that gate to all 18 representable
   Rebar rows. The current checked-in result is 9/18 on each host.

The Rebar evidence is in [`audit/rebar/`](audit/rebar/README.md). The
36-row receipts in [`audit/acceptance/`](audit/acceptance/README.md) predate the
rule that every pinned entrant counts, so they do not meet gate 6.

## Limits

- The measured result covers Intel x86-64 with AVX-512F/BW/VBMI.
- AVX2 and scalar paths preserve semantics. They do not carry the published
  speed claim.
- ARM64 uses the portable scalar path. There is no NEON kernel.
- The search relation is Unicode simple folding. Full-fold expansions are out
  of scope.
- The API accepts literals and finite literal sets.
- Adversarial text can create many filter survivors. The arena includes
  same-byte, periodic, torture, width-changing, dense-hit, and late-hit rows.

## Map to the code

- [`plan.go`](plan.go) compiles fold tokens, the shared state machine, and
  route selection.
- [`unicode_confirm.go`](unicode_confirm.go) packs and checks fixed- and
  variable-width raw confirmations.
- [`raw_byte.go`](raw_byte.go) builds tagged multi-pattern anchors and the
  common-byte origin gate.
- [`matcher.go`](matcher.go) exposes `Find` and `Each` over the same plan.
- [`root_amd64.go`](root_amd64.go) performs runtime feature dispatch.
- [`root_amd64.s`](root_amd64.s) contains the AVX2 and AVX-512 kernels.
- [`root_other.go`](root_other.go) implements the portable filter contracts.
- [`arena/field.yaml`](arena/field.yaml) defines the field, semantics, and
  dispatch requirements.

</details>
