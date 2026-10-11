# rure dispatch audit patches

`prepare.sh` first fetches the pinned crates through Cargo's checksum-verified
registry path, then copies their sources into its caller-owned build root.
These two small patches apply only to those copies. The pinned direct Rust
Aho-Corasick entrant shares `memchr-dispatch-audit.patch` for its own separately
staged `memchr` 2.8.3 copy:

- `memchr-dispatch-audit.patch` records the widest backend reached by a search
  on the calling native thread (AVX2, SSE2, or fallback).
- `rure-dispatch-audit.patch` adds a private C entry point that resets and
  reads that state around one Rust regex search.  The reset/read pair lives in one
  native call because separate cgo calls are not guaranteed to use the same OS
  thread.

The Go adapter reports the width each query reached as a diagnostic, observed
rather than labeled from Go CPU flags. The width never removes rure from the
field: it counts toward `x_vs_best` on every row (#29, #31).
