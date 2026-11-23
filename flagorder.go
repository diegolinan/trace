package main

import (
	"flag"
	"strings"
)

// flagAfterWords is the name of the first word left after the flags that names one of trace's flags (-home, --home,
// -home=X), or "". Go stops reading flags at the first word, so such a flag would otherwise be searched for as a word
// and the default home opened in silence (SPEC E6). A word that only starts with "-" (-5%, -foo) stays a word.
func flagAfterWords(fs *flag.FlagSet) string {
	for _, w := range fs.Args() {
		name, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(w, "-"), "-"), "=")
		if strings.HasPrefix(w, "-") && fs.Lookup(name) != nil {
			return name
		}
	}
	return ""
}
