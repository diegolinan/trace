package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Pins headTail: short text is kept whole; long text keeps about 1,500 bytes at each end, cut on a character
// boundary whatever the character's width, with a marker that counts what was cut.
func TestHeadTail(t *testing.T) {
	if s := strings.Repeat("a", 3200); headTail(s) != s {
		t.Error("text under the limit was cut")
	}
	for _, wide := range []string{"é", "€", "𝄞", "𝄞€", "é𝄞", "€é"} { // 2, 3 and 4 bytes, and mixed widths
		for shift := 0; shift < 8; shift++ {
			s := strings.Repeat("a", 1500-shift) + strings.Repeat(wide, 2000) + strings.Repeat("b", 1500-shift)
			got := headTail(s)
			if !utf8.ValidString(got) {
				t.Fatalf("%q shift %d: the result is not valid UTF-8", wide, shift)
			}
			// The oracle: the longest valid prefix and suffix of at most 1,500 bytes.
			wantHead, wantTail := s[:1500], s[len(s)-1500:]
			for !utf8.ValidString(wantHead) {
				wantHead = wantHead[:len(wantHead)-1]
			}
			for !utf8.ValidString(wantTail) {
				wantTail = wantTail[1:]
			}
			head, tail, ok := strings.Cut(got, "\n[… ")
			if !ok || head != wantHead {
				t.Fatalf("%q shift %d: head of %d bytes, want %d", wide, shift, len(head), len(wantHead))
			}
			tail = tail[strings.Index(tail, "…]\n")+len("…]\n"):]
			if tail != wantTail {
				t.Fatalf("%q shift %d: tail of %d bytes, want %d", wide, shift, len(tail), len(wantTail))
			}
			if want := len(s) - len(head) - len(tail); !strings.Contains(got, "[… "+itoa(want)+" chars cut") {
				t.Fatalf("%q shift %d: the marker does not count %d", wide, shift, want)
			}
		}
	}
}
