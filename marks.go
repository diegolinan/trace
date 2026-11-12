package main

import (
	"regexp"
	"strings"
)

// Which of the user's typed messages approve, reject, correct or set a standing rule, by code. Markers
// count only at the start of a clause (muninn's Spanish and English markers), after folding case and accents so
// voseo typed without accents matches ("pará", "para"). Requests to explain are not corrections. The rules are scored
// against Codex's blind labels of 200 real messages in TestMarksAgainstLabels.

var (
	pasted  = regexp.MustCompile(`(?s)<pasted_content[^>]*>.*?</pasted_content[^>]*>`)
	clauses = regexp.MustCompile(`[.!?;,:\n]+|\s-\s|\.\.\.`)
	fold    = strings.NewReplacer("á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u", "Á", "a", "É", "e", "Í", "i", "Ó", "o", "Ú", "u")

	// The first clause answers what the assistant just asked or proposed.
	accentSi  = regexp.MustCompile(`(^|[^\p{L}])sí([^\p{L}]|$)`) // "sí" is yes; "si" is usually "if"
	tagNo     = regexp.MustCompile(`,?\s*\bno\s*\?`)             // "..., no?" asks, it does not refuse
	filler    = regexp.MustCompile(`^(?:a|ah|bueno|mm+|uh|che|(?:ja)+j?)$`)
	siOnly    = regexp.MustCompile(`^(?:si+|sip|si a todo)$`)
	approveRe = regexp.MustCompile(`^(?:dale|yes+|yup|yep|ok+|okey|okay|va|bien|allowed|like it|segui|hacelo|metele|de acuerdo|estamos de acuerdo|perfecto|genial|exacto|sale|adelante|aprobado|correcto|go|joya)\b`)
	// A clause that is only a stop word, or starts with a negative order.
	stopRe   = regexp.MustCompile(`^(?:para|pare|frena|banca|wait|stop|basta)(?: (?:para|pare|frena|banca|wait|stop|basta))*$`)
	bareNo   = regexp.MustCompile(`^(?:no+|nono|nop)$`)
	rejectRe = regexp.MustCompile(`^(?:no te adelantes|no hagas|no toques|no sigas|no lo hagas|no cambies|no borres|no hace falta|ni se te ocurra|\S+ no$|banca|ya no|todavia no|no por ahora|no todavia|saca(?:lo|la)?|olvidate|do not|don't|dont|stop|never mind)\b`)
	fixRe    = regexp.MustCompile(`^(?:te dije|ya te dije|eso no|no es asi|no era|no es eso|en vez de|me referia|me refiero|te olvidaste|olvidaste|esta mal|estas mal|no funciona|no anda|no andan|dijimos|te equivocaste|al reves|wrong|that's wrong|not what i|i meant|no hizo nada|lo hizo todo mal|no,? es|no se vio)\b`)
	ruleRe   = regexp.MustCompile(`^(?:a partir de ahora|de aca en mas|de ahora en mas|desde ahora|siempre|nunca|no vuelvas a|from now on|always|never|de a una cosa por vez|la regla es)\b`)
	explain  = regexp.MustCompile(`^(?:no entiendo|no se|no te entiendo|explicame|que es|what is|i don't understand)\b`)
)

type marks struct{ correction, rejection, approval, rule bool }

// lastingRe is the part of rejectRe that refuses something beyond the turn: negative orders and "X no". Left out:
// interrupts (para, banca, stop), "sacalo" (an order to remove, as often an approval as a refusal) and "ya no"
// (as often confusion). Seen on real boots, where those filled the list with non-refusals.
var lastingRe = regexp.MustCompile(`^(?:no te adelantes|no hagas|no toques|no sigas|no lo hagas|no cambies|no borres|no hace falta|ni se te ocurra|\S+ no$|todavia no|no por ahora|no todavia|olvidate|do not|don't|dont|never mind)\b`)

// lasting is true when a typed message refuses something or sets a rule that should outlive the session.
func lasting(text string) bool {
	if markText(text).rule {
		return true
	}
	t := fold.Replace(strings.ToLower(pasted.ReplaceAllString(text, " ")))
	t = tagNo.ReplaceAllString(t, "?")
	for _, c := range clauses.Split(t, -1) {
		if c = strings.Join(strings.Fields(c), " "); c != "" && !explain.MatchString(c) && lastingRe.MatchString(c) {
			return true
		}
	}
	return false
}

func markText(text string) marks {
	t := strings.ToLower(pasted.ReplaceAllString(text, " "))
	t = fold.Replace(tagNo.ReplaceAllString(accentSi.ReplaceAllString(t, "${1}yes${2}"), "?"))
	var cs []string
	for _, c := range clauses.Split(t, -1) {
		if c = strings.Join(strings.Fields(c), " "); c != "" && !filler.MatchString(c) {
			cs = append(cs, c)
		}
	}
	var m marks
	first := true
	for i, c := range cs {
		// A lone "no" refuses; "no, <the right version>" corrects.
		if bareNo.MatchString(c) {
			if i == len(cs)-1 {
				m.rejection = true
			} else {
				m.correction = true
			}
			first = false
			continue
		}
		if explain.MatchString(c) {
			first = false
			continue
		}
		if first && (approveRe.MatchString(c) || siOnly.MatchString(c)) {
			m.approval = true
		}
		first = false
		if stopRe.MatchString(c) || rejectRe.MatchString(c) {
			m.rejection = true
		}
		if fixRe.MatchString(c) {
			m.correction = true
		}
		if ruleRe.MatchString(c) {
			m.rule = true
		}
	}
	return m
}
