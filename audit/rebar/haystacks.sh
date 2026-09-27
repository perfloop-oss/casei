#!/bin/sh
# Fetch the Rebar haystacks used by the 18 casei rows into DEST.
#
# Every file comes from Rebar at the pinned audit commit and must match the
# sha256 recorded below. Files already present with the right hash are kept.
# A file with the wrong hash, before or after download, stops the script.
#
# usage: audit/rebar/haystacks.sh DEST
set -eu

REBAR_COMMIT=463d00f31887e84c38467805b9e3122c314b9521
BASE="https://raw.githubusercontent.com/BurntSushi/rebar/$REBAR_COMMIT/benchmarks/haystacks"

if [ "$#" -ne 1 ]; then
	echo "usage: $0 DEST" >&2
	exit 2
fi
dest=$1

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

# fetch PATH SHA256 downloads one haystack unless a verified copy exists.
fetch() {
	path=$1
	want=$2
	out="$dest/$path"
	if [ -f "$out" ]; then
		got=$(sha256 "$out")
		if [ "$got" = "$want" ]; then
			echo "ok      $path"
			return
		fi
		echo "haystacks: $out has sha256 $got, want $want; remove it and rerun" >&2
		exit 1
	fi
	mkdir -p "$(dirname "$out")"
	curl -fsSL --retry 3 -o "$out.tmp" "$BASE/$path"
	got=$(sha256 "$out.tmp")
	if [ "$got" != "$want" ]; then
		rm -f "$out.tmp"
		echo "haystacks: downloaded $path has sha256 $got, want $want" >&2
		exit 1
	fi
	mv "$out.tmp" "$out"
	echo "fetched $path"
}

fetch imported/leipzig-3200.txt f2aa28234e7a8212c9e009fa9c67d1960d2d063d076765de46b0faed5fe44ad8
fetch opensubtitles/en-huge.txt 07ff024bdc05f6c2b4bc0b5b768a332a18a616261fcbd16b41e953df1c7fa7ff
fetch opensubtitles/en-sampled.txt 0d40805f6d02c8fe02bd75945b98911891f707e8ecb939e018446858065d76ea
fetch opensubtitles/ru-huge.txt 40d93a4618e69e81c063902106c243759f1bb08b48bdf593a288c386b0d9fe0c
fetch opensubtitles/ru-sampled.txt 7ffddb21336a1bfb4a9e2df4bb77eea0305c0010a57c5d3c56e0dfead9e80a90
fetch sherlock.txt 242ec73a70f0a03dcbe007e32038e7deeaee004aaec9a09a07fa322743440fa8
