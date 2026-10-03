// Package normalize implements the shared text normalisation pipeline
// (doc 07 §3) used by categorisation, dedupe and the transfer detector.
package normalize

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	cardChunks   = regexp.MustCompile(`\b\d{4}[\s-]?\d{4}[\s-]?\d{4}\b`)
	isoTimestamp = regexp.MustCompile(`\d{2}-\d{2}-\d{2}T\d{2}:\d{2}`)
	longDigits   = regexp.MustCompile(`\d{5,}`)
	trailingNums = regexp.MustCompile(`\s+\d+(\s+\d+)*$`)
)

// Text normalises a statement description (doc 07 §3):
//  1. lowercase + trim
//  2. strip card chunks / ISO timestamps / long digit runs
//  3. strip UK payment suffixes
//  4. collapse whitespace/punctuation
//  5. drop trailing number tokens
func Text(desc string) string {
	s := strings.ToLower(strings.TrimSpace(desc))
	s = cardChunks.ReplaceAllString(s, " ")
	s = isoTimestamp.ReplaceAllString(s, " ")
	s = longDigits.ReplaceAllString(s, "#")
	s = stripSuffixes(s)
	var b strings.Builder
	prevSpace := true // trim leading
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '#' {
			b.WriteRune(r)
			prevSpace = false
		} else if !prevSpace {
			b.WriteByte(' ')
			prevSpace = true
		}
	}
	s = strings.TrimSpace(b.String())
	s = trailingNums.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

var suffixes = []string{
	"faster payment", "fps", "sepa", "dd", "so", "tfr", "via card", "web", "pos",
	"bacs payment received", "bacs payment", "direct debit", "standing order",
}

func stripSuffixes(s string) string {
	for _, suf := range suffixes {
		p := " " + suf + " "
		s = strings.ReplaceAll(s, p, " ")
		if strings.HasPrefix(s, suf+" ") {
			s = s[len(suf)+1:]
		}
		if strings.HasPrefix(s, suf) {
			s = s[len(suf):]
		}
	}
	return s
}

// Tokens splits normalised text into unigrams.
func Tokens(desc string) []string {
	return strings.Fields(Text(desc))
}

// Bigrams returns unigrams + adjacent bigrams (classifier features).
func Bigrams(desc string) []string {
	toks := strings.Fields(Text(desc))
	out := make([]string, 0, len(toks)*2)
	out = append(out, toks...)
	for i := 0; i+1 < len(toks); i++ {
		out = append(out, toks[i]+"_"+toks[i+1])
	}
	return out
}
