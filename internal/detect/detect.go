// Package detect finds text a screen recording should not show. Pure string work: no OCR, no
// images. Offsets are rune indices into the given text, so a caller can map them to OCR words
// whatever the script (Vietnamese letters are one rune, several bytes).
package detect

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Match is one finding: runes [Start, End) of the text.
type Match struct {
	Kind  string `json:"kind"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

var DefaultKinds = []string{"email", "api-key", "jwt", "private-key", "credit-card", "phone", "secret-assignment", "url-credentials", "high-entropy"}

var (
	reEmail   = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)
	reAPIKeys = []*regexp.Regexp{
		regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}`),
		regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{40,}`),
		regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
		regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`),
		regexp.MustCompile(`\b[sr]k_live_[A-Za-z0-9]{16,}`),
		regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}\b`),
		regexp.MustCompile(`\bBearer\s+[A-Za-z0-9._~+/=-]{20,}`),
	}
	reJWT        = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)
	rePrivateKey = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)
	reCard       = regexp.MustCompile(`\b(?:\d[ -]?){12,18}\d\b`)
	// Phone numbers must not sit inside a longer number: checked around each match (RE2 has no
	// lookaround).
	rePhoneVN   = regexp.MustCompile(`(?:\+84|84|0)[\s.-]?[35789](?:[\s.-]?\d){8}`)
	rePhoneIntl = regexp.MustCompile(`\+\d{1,3}(?:[\s.-]\d{2,4}){2,4}`)
	reIPv4      = regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4]\d|1?\d?\d)\.){3}(?:25[0-5]|2[0-4]\d|1?\d?\d)\b`)
	// Labels in English and Vietnamese, with and without diacritics (OCR without the vie pack
	// drops them). The label must not follow a letter or digit: checked around each match.
	secretLabel = `(?:[a-z0-9]*_(?:key|token|secret|pass(?:word)?|pwd)|pass(?:word|wd)?|pwd|secret|token|api[_-]?key|apikey|access[_-]?key|private[_-]?key|authorization|auth[_-]?token|m[aậ]t\s*kh[aẩ]u|m[aậ]t\s*m[aã]|m[aã]\s*(?:pin|otp|b[ií]\s*m[aậ]t|x[aá]c\s*th[uự]c)|kh[oó]a\s*b[ií]\s*m[aậ]t)`
	reSecret    = regexp.MustCompile(`(?i)` + secretLabel + `\w*\s*[:=]\s*["']?([^\s"']{4,})`)
	// An environment-style label (DB_PASSWORD, STRIPE_KEY...) whose "=" OCR lost: "DB_PASSWORD
	// rOngDong2026x". Only taken when the next word looks like a secret (letters and digits),
	// since without the "=" the label could be prose.
	reEnvNoSep = regexp.MustCompile(`(?i)[a-z0-9]*_(?:key|token|secret|pass(?:word)?|pwd)\s+["']?([^\s"'=:]{6,})`)
	// A value OCR split after its first letters: "DB_PASSWORD=T rOngDong2026x". Taken when the
	// second piece looks like a secret (letters and digits).
	reSecretSplit = regexp.MustCompile(`(?i)` + secretLabel + `\w*\s*[:=]\s*["']?([^\s"']{1,3}\s+([^\s"']{4,}))`)
	reURLCred     = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s/:@]+:([^\s/@]+)@`)
	reTokenish    = regexp.MustCompile(`[A-Za-z0-9+/_=-]{32,}`)
	reSlug        = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)+$`)
	reFourDigit   = regexp.MustCompile(`\d{4}`)
	reDigitRun    = regexp.MustCompile(`[0-9OoDQlI|!SsBZz][0-9OoDQlI|!SsBZz .-]{6,}[0-9OoDQlI|!SsBZz]`)
	reLostAt      = regexp.MustCompile(`[A-Za-z0-9._%+-]{2,}\.[A-Za-z]{2,}\b`)
	reSpaced      = regexp.MustCompile(`[A-Za-z0-9._%+-]+(?: [A-Za-z0-9._%+-]+){0,2} ?@ ?[A-Za-z0-9-]+(?: ?[A-Za-z0-9.-]+){0,4}`)
	reEmailOnly   = regexp.MustCompile(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.([A-Za-z]{2,})$`)
	reDots        = regexp.MustCompile(`\.{2,}`)
)

// Characters OCR returns for look-alike Latin letters. Each maps to one character, so rune
// offsets into the folded text are rune offsets into the original.
var ocrFold = map[rune]rune{'¥': 'Y', '€': 'E', '§': 'S', '£': 'E', '©': 'c', '®': 'R', '¢': 'c', 'µ': 'u', 'ß': 'B', '|': 'l', '—': '-', '–': '-', '‘': '\'', '’': '\'', '“': '"', '”': '"'}

// Letters OCR returns inside numbers. Only applied to runs that are mostly digits (card and
// phone numbers), never to words, where "S" and "O" are real letters.
var digitFold = map[byte]byte{'O': '0', 'o': '0', 'D': '0', 'Q': '0', 'l': '1', 'I': '1', '|': '1', '!': '1', 'S': '5', 's': '5', 'B': '8', 'Z': '2', 'z': '2'}

// FoldOCR undoes common OCR substitutions, keeping the number of runes (and so every offset).
func FoldOCR(text string) string {
	var b strings.Builder
	for _, r := range text {
		if f, ok := ocrFold[r]; ok {
			r = f
		}
		b.WriteRune(r)
	}
	// Digit-heavy runs like "4111 1l11 1111 1lll" or "0912 34S 678" (all ASCII: same length).
	return reDigitRun.ReplaceAllStringFunc(b.String(), func(run string) string {
		digits, chars := 0, 0
		for i := 0; i < len(run); i++ {
			c := run[i]
			if c >= '0' && c <= '9' {
				digits++
			}
			if c != ' ' && c != '.' && c != '-' {
				chars++
			}
		}
		if float64(digits)/float64(chars) < 0.6 {
			return run
		}
		out := []byte(run)
		for i, c := range out {
			if d, ok := digitFold[c]; ok {
				out[i] = d
			}
		}
		return string(out)
	})
}

// Domains an address realistically ends with. Used only when OCR lost the "@", to tell
// "jane.doegexample.com" (an address) from "logging.info" (code).
var emailTLDs = map[string]bool{"com": true, "net": true, "org": true, "io": true, "vn": true, "co": true, "dev": true, "ai": true, "edu": true, "gov": true, "me": true, "app": true, "uk": true, "us": true, "de": true, "fr": true, "jp": true, "kr": true, "cn": true, "au": true, "ca": true, "in": true, "sg": true, "biz": true}

// byteSpan is a finding in byte offsets, converted to runes at the end.
type byteSpan struct {
	kind       string
	start, end int
}

// Addresses OCR broke with spaces: "Jjane.. doe@examp Le. com".
func findSpacedEmails(text string, out *[]byteSpan) {
	for _, loc := range reSpaced.FindAllStringIndex(text, -1) {
		m := text[loc[0]:loc[1]]
		if !strings.Contains(m, " ") {
			continue
		}
		// Drop words before the address ("Contact Jjane.."), keep the pieces glued by "." or "@".
		pieces := strings.Split(m, " ")
		skip := 0
		for first := range pieces {
			candidate := reDots.ReplaceAllString(strings.Join(pieces[first:], ""), ".")
			e := reEmailOnly.FindStringSubmatch(candidate)
			if e != nil && emailTLDs[strings.ToLower(e[1])] {
				*out = append(*out, byteSpan{"email", loc[0] + skip, loc[1]})
				break
			}
			skip += len(pieces[first]) + 1
		}
	}
}

// Addresses whose "@" OCR read as "g".
func findLostAtEmails(text string, out *[]byteSpan) {
	for _, loc := range reLostAt.FindAllStringIndex(text, -1) {
		token := text[loc[0]:loc[1]]
		if strings.Contains(token, "@") {
			continue
		}
		for i := 1; i < len(token)-4; i++ {
			if token[i] != 'g' {
				continue
			}
			candidate := token[:i] + "@" + token[i+1:]
			e := reEmailOnly.FindStringSubmatch(candidate)
			if e == nil || !emailTLDs[strings.ToLower(e[1])] {
				continue
			}
			local, domain, _ := strings.Cut(candidate, "@")
			// The domain needs a real name before the TLD, the local part a real name too. Two
			// labels (example.com), or three under a country's second level (fpt.com.vn): a
			// longer name is a host ("staging.example.com"), not an address that lost its "@".
			labels := strings.Split(domain, ".")
			if len(local) < 2 || len(labels[0]) < 2 {
				continue
			}
			if len(labels) > 3 || (len(labels) == 3 && !map[string]bool{"com": true, "co": true, "edu": true, "gov": true, "org": true, "net": true, "ac": true}[labels[1]]) {
				continue
			}
			*out = append(*out, byteSpan{"email", loc[0], loc[1]})
			break
		}
	}
}

// LuhnValid checks a card number's check digit.
func LuhnValid(digits string) bool {
	sum, double := 0, false
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i]) - '0'
		if d < 0 || d > 9 {
			return false
		}
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum > 0 && sum%10 == 0
}

// ShannonEntropy in bits per character.
func ShannonEntropy(text string) float64 {
	runes := []rune(text)
	if len(runes) == 0 {
		return 0
	}
	counts := map[rune]int{}
	for _, r := range runes {
		counts[r]++
	}
	bits := 0.0
	for _, n := range counts {
		p := float64(n) / float64(len(runes))
		bits -= p * math.Log2(p)
	}
	return bits
}

func randomLooking(token string) bool {
	if !strings.ContainsFunc(token, func(r rune) bool { return r < 128 && unicode.IsLetter(r) }) || !strings.ContainsFunc(token, unicode.IsDigit) {
		return false
	}
	if reSlug.MatchString(token) && !reFourDigit.MatchString(token) {
		return false
	}
	return ShannonEntropy(token) >= 3.6
}

func collect(text string, re *regexp.Regexp, kind string, out *[]byteSpan, accept func(string) bool, group int) {
	for _, loc := range re.FindAllStringSubmatchIndex(text, -1) {
		s, e := loc[2*group], loc[2*group+1]
		if s < 0 || s == e {
			continue
		}
		if accept != nil && !accept(text[loc[0]:loc[1]]) {
			continue
		}
		*out = append(*out, byteSpan{kind, s, e})
	}
}

// collectGuarded is collect for patterns that had lookarounds: a match whose surroundings fail
// `ok` is dropped and the search resumes one character later, as a lookbehind would.
func collectGuarded(text string, re *regexp.Regexp, kind string, out *[]byteSpan, ok func(before, after rune) bool, group int) {
	for pos := 0; pos < len(text); {
		loc := re.FindStringSubmatchIndex(text[pos:])
		if loc == nil {
			return
		}
		for i := range loc {
			if loc[i] >= 0 {
				loc[i] += pos
			}
		}
		before, after := rune(0), rune(0)
		if loc[0] > 0 {
			before = lastRune(text[:loc[0]])
		}
		if loc[1] < len(text) {
			after = []rune(text[loc[1]:min(len(text), loc[1]+4)])[0]
		}
		if !ok(before, after) {
			pos = loc[0] + runeLen(text[loc[0]:])
			continue
		}
		if s, e := loc[2*group], loc[2*group+1]; s >= 0 && s < e {
			*out = append(*out, byteSpan{kind, s, e})
		}
		pos = max(loc[1], loc[0]+1)
	}
}

func lastRune(s string) rune {
	r := []rune(s)
	return r[len(r)-1]
}

func runeLen(s string) int {
	for i := range s {
		if i > 0 {
			return i
		}
	}
	return len(s)
}

// When two findings overlap, the merged one takes the more dangerous kind: a password in a URL
// ("app:S3cr3t@db.internal") also looks like an email, and must be hidden like a password.
var severity = []string{"phone", "email", "ip-address", "high-entropy", "credit-card", "url-credentials", "secret-assignment", "jwt", "api-key", "private-key"}

func rank(kind string) int {
	for i, k := range severity {
		if k == kind {
			return i
		}
	}
	return -1
}

func dedupe(spans []byteSpan) []byteSpan {
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].end > spans[j].end
	})
	var kept []byteSpan
	for _, m := range spans {
		if n := len(kept); n > 0 && m.start < kept[n-1].end {
			last := &kept[n-1]
			if m.end > last.end {
				last.end = m.end
			}
			if rank(m.kind) > rank(last.kind) {
				last.kind = m.kind
			}
			continue
		}
		kept = append(kept, m)
	}
	return kept
}

func notDigit(r rune) bool { return r < '0' || r > '9' }

// Find returns the sensitive spans of text, in rune offsets, merged where they overlap.
// kinds nil means DefaultKinds.
func Find(text string, kinds []string) []Match {
	if kinds == nil {
		kinds = DefaultKinds
	}
	on := map[string]bool{}
	for _, k := range kinds {
		on[k] = true
	}
	var out []byteSpan
	if on["private-key"] {
		collect(text, rePrivateKey, "private-key", &out, nil, 0)
	}
	if on["jwt"] {
		collect(text, reJWT, "jwt", &out, nil, 0)
	}
	if on["api-key"] {
		for _, re := range reAPIKeys {
			collect(text, re, "api-key", &out, nil, 0)
		}
	}
	if on["url-credentials"] {
		collect(text, reURLCred, "url-credentials", &out, nil, 1)
	}
	if on["secret-assignment"] {
		collectGuarded(text, reSecret, "secret-assignment", &out, func(before, _ rune) bool {
			return !unicode.IsLetter(before) && !unicode.IsDigit(before)
		}, 1)
		var env []byteSpan
		collectGuarded(text, reEnvNoSep, "secret-assignment", &env, func(before, _ rune) bool {
			return !unicode.IsLetter(before) && !unicode.IsDigit(before)
		}, 1)
		for _, loc := range findAllGuarded(text, reSecretSplit) {
			second := text[loc[4]:loc[5]]
			if strings.ContainsFunc(second, unicode.IsLetter) && strings.ContainsFunc(second, unicode.IsDigit) {
				out = append(out, byteSpan{"secret-assignment", loc[2], loc[3]})
			}
		}
		for _, sp := range env {
			v := text[sp.start:sp.end]
			if strings.ContainsFunc(v, unicode.IsLetter) && strings.ContainsFunc(v, unicode.IsDigit) {
				out = append(out, sp)
			}
		}
	}
	if on["email"] {
		collect(text, reEmail, "email", &out, nil, 0)
		findLostAtEmails(text, &out)
		findSpacedEmails(text, &out)
	}
	if on["credit-card"] {
		collect(text, reCard, "credit-card", &out, func(m string) bool {
			d := strings.NewReplacer(" ", "", "-", "").Replace(m)
			return len(d) >= 13 && len(d) <= 19 && LuhnValid(d)
		}, 0)
	}
	if on["phone"] {
		guard := func(before, after rune) bool { return notDigit(before) && notDigit(after) }
		collectGuarded(text, rePhoneVN, "phone", &out, guard, 0)
		collectGuarded(text, rePhoneIntl, "phone", &out, guard, 0)
	}
	if on["ip-address"] {
		collect(text, reIPv4, "ip-address", &out, nil, 0)
	}
	if on["high-entropy"] {
		collect(text, reTokenish, "high-entropy", &out, randomLooking, 0)
	}
	spans := dedupe(out)
	// Byte offsets to rune offsets (every span starts and ends on a rune boundary).
	runeAt := make(map[int]int, len(text)+1)
	n := 0
	for i := range text {
		runeAt[i] = n
		n++
	}
	runeAt[len(text)] = n
	matches := make([]Match, len(spans))
	for i, s := range spans {
		matches[i] = Match{Kind: s.kind, Start: runeAt[s.start], End: runeAt[s.end]}
	}
	return matches
}

// MaskedPreview identifies a match to a human without printing it: "sk…(41)".
func MaskedPreview(value string) string {
	r := []rune(value)
	if len(r) <= 4 {
		return "…(" + strconv.Itoa(len(r)) + ")"
	}
	return string(r[:2]) + "…(" + strconv.Itoa(len(r)) + ")"
}

// findAllGuarded: the submatch indexes of every match not preceded by a letter or digit
// (the lookbehind RE2 lacks).
func findAllGuarded(text string, re *regexp.Regexp) [][]int {
	var out [][]int
	for pos := 0; pos < len(text); {
		loc := re.FindStringSubmatchIndex(text[pos:])
		if loc == nil {
			break
		}
		for i := range loc {
			if loc[i] >= 0 {
				loc[i] += pos
			}
		}
		if loc[0] > 0 {
			if b := lastRune(text[:loc[0]]); unicode.IsLetter(b) || unicode.IsDigit(b) {
				pos = loc[0] + runeLen(text[loc[0]:])
				continue
			}
		}
		out = append(out, loc)
		pos = max(loc[1], loc[0]+1)
	}
	return out
}
