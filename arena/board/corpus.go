package board

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The arena's corpora. Each builder is a deterministic function of its random
// source, so BenchmarkBar passes its fixed source and gets the bytes it has
// always measured, while the board passes one stream per cell. The word lists
// and line formats below are the pinned text; nothing is read from disk or the
// network.

var logLevels = []string{"DEBUG", "INFO", "INFO", "INFO", "WARN", "ERROR"}
var logServices = []string{"checkout", "search", "ingest", "billing", "Gateway", "AuthZ", "replicator"}
var logMessages = []string{
	"request completed", "cache miss", "retry scheduled", "connection reset by peer",
	"slow query detected", "Payment Authorized", "token refreshed", "queue depth high",
	"compaction finished", "TLS handshake complete", "rate limit applied",
}

// Logs builds size bytes of structured service log lines.
func Logs(rng *rand.Rand, size int) string {
	var b strings.Builder
	b.Grow(size + 256)
	for b.Len() < size {
		fmt.Fprintf(&b, "2026-08-04T%02d:%02d:%02d.%03dZ %s service=%s region=eu-west-%d trace=%08x%08x msg=%q latency_ms=%d\n",
			rng.IntN(24), rng.IntN(60), rng.IntN(60), rng.IntN(1000),
			logLevels[rng.IntN(len(logLevels))],
			logServices[rng.IntN(len(logServices))],
			1+rng.IntN(3), rng.Uint32(), rng.Uint32(),
			logMessages[rng.IntN(len(logMessages))],
			rng.IntN(2000))
	}
	return b.String()[:size]
}

var proseWords = strings.Fields(`the of and to in a is that it was for on are with as his they at be
this have from or one had by word but not what all were we when your can said there use an each which
she do how their if will up other about out many then them these so some her would make like him into
time has look two more write go see number no way could people my than first water been call who oil
its now find long down day did get come made may part over new sound take only little work know place
year live me back give most very after thing our just name good sentence man think say great where
help through much before line right too mean old any same tell boy follow came want show also around
form three small set put end does another well large must big even such because turn here why ask went
men read need land different home us move try kind hand picture again change off play spell air away
animal house point page letter mother answer found study still learn should America world`)

var cyrillicWords = strings.Fields(`доктор ватсон улица бейкер лондон туман дело улика письмо газета
вечер утро дверь окно комната огонь свеча тень шаг голос вопрос ответ время город река мост камень
дождь ветер ночь свет тайна встреча друг враг правда история конец начало Инспектор Лестрейд`)

// Prose builds English sentences from the most common English words.
func Prose(rng *rand.Rand, size int) string { return words(rng, proseWords, size) }

// Russian builds Cyrillic sentences. Every letter is a two-byte rune.
func Russian(rng *rand.Rand, size int) string { return words(rng, cyrillicWords, size) }

// words builds capitalized sentences of 6 to 14 words. The result is trimmed to
// a rune boundary at or below size, so it stays valid UTF-8.
func words(rng *rand.Rand, list []string, size int) string {
	var b strings.Builder
	b.Grow(size + 128)
	for b.Len() < size {
		n := 6 + rng.IntN(9)
		for i := 0; i < n; i++ {
			w := list[rng.IntN(len(list))]
			if i == 0 {
				r, sz := utf8.DecodeRuneInString(w)
				w = string(unicode.ToUpper(r)) + w[sz:]
			}
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(w)
		}
		b.WriteString(". ")
	}
	s := b.String()
	for size > 0 && size < len(s) && s[size]&0xC0 == 0x80 {
		size--
	}
	return s[:size]
}

// Code builds size bytes of Go-like source code.
func Code(rng *rand.Rand, size int) string {
	var b strings.Builder
	b.Grow(size + 256)
	for b.Len() < size {
		id := rng.IntN(10000)
		fmt.Fprintf(&b, "func handleReq%d(ctx context.Context, in []byte) (map[string]int, error) {\n", id)
		fmt.Fprintf(&b, "\tout := make(map[string]int, %d)\n\tfor i, v := range in {\n", rng.IntN(64))
		fmt.Fprintf(&b, "\t\tif v&0x%02x != 0 {\n\t\t\tout[keys[i%%%d]] += int(v)\n\t\t}\n\t}\n", rng.IntN(256), 1+rng.IntN(16))
		fmt.Fprintf(&b, "\tif len(out) == 0 {\n\t\treturn nil, fmt.Errorf(\"empty%d: %%w\", errSentinel)\n\t}\n\treturn out, nil\n}\n\n", id)
	}
	return b.String()[:size]
}
