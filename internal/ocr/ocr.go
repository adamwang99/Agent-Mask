// Package ocr reads text through the tesseract command (TSV output), then rebuilds text rows
// from word geometry so "password:" and its value stay together whatever layout tesseract chose.
package ocr

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Word is one word tesseract read, in pixels.
type Word struct {
	Text           string
	X0, Y0, X1, Y1 float64
	Conf           float64
}

var tsvHeader = regexp.MustCompile(`^level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext`)

// ParseTSV reads tesseract TSV. It fails when the output is not TSV at all: a misconfigured
// tesseract (TESSDATA_PREFIX without its configs/ folder) prints plain text instead, which read
// as TSV is "no words", and a screen full of secrets would pass as clean.
func ParseTSV(tsv string) ([]Word, error) {
	if !tsvHeader.MatchString(tsv) {
		head := strings.SplitN(strings.TrimSpace(tsv), "\n", 2)[0]
		if len(head) > 80 {
			head = head[:80]
		}
		return nil, fmt.Errorf("tesseract did not return TSV (got %q). Check TESSDATA_PREFIX: it must point to a tessdata folder that also contains configs/tsv", head)
	}
	var words []Word
	lines := strings.Split(strings.ReplaceAll(tsv, "\r\n", "\n"), "\n")
	for _, line := range lines[1:] {
		c := strings.Split(line, "\t")
		if len(c) < 12 || c[0] != "5" {
			continue
		}
		text := strings.TrimSpace(strings.Join(c[11:], "\t"))
		conf, err := strconv.ParseFloat(c[10], 64)
		if text == "" || err != nil || conf < 0 {
			continue
		}
		num := func(s string) float64 { v, _ := strconv.ParseFloat(s, 64); return v }
		left, top := num(c[6]), num(c[7])
		words = append(words, Word{Text: text, X0: left, Y0: top, X1: left + num(c[8]), Y1: top + num(c[9]), Conf: conf})
	}
	return words, nil
}

// Span is where a word sits in a row's text, in runes.
type Span struct{ Start, End int }

// Row is a run of words on one line, in reading order, and their text.
type Row struct {
	Words []Word
	Text  string
	Spans []Span
}

// BuildRows groups words into visual rows (same baseline band), split into segments where a
// wide gap separates columns. No confidence filter by default: tesseract scores random strings
// low because they match no dictionary word, so a filter throws away exactly the API keys.
func BuildRows(words []Word, minConf, columnGap float64) []Row {
	if columnGap == 0 {
		columnGap = 3
	}
	var kept []Word
	for _, w := range words {
		if w.Conf >= minConf {
			kept = append(kept, w)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].Y0+kept[i].Y1 < kept[j].Y0+kept[j].Y1 })
	type line struct {
		cy, h float64
		words []Word
	}
	var lines []*line
	for _, w := range kept {
		cy, h := (w.Y0+w.Y1)/2, w.Y1-w.Y0
		var row *line
		for _, r := range lines {
			if abs(r.cy-cy) < 0.5*max(r.h, h) {
				row = r
				break
			}
		}
		if row != nil {
			row.words = append(row.words, w)
			n := float64(len(row.words))
			row.cy = (row.cy*(n-1) + cy) / n
			row.h = max(row.h, h)
		} else {
			lines = append(lines, &line{cy, h, []Word{w}})
		}
	}
	var segments [][]Word
	for _, r := range lines {
		sort.SliceStable(r.words, func(i, j int) bool { return r.words[i].X0 < r.words[j].X0 })
		var cur []Word
		for _, w := range r.words {
			if len(cur) > 0 && w.X0-cur[len(cur)-1].X1 > columnGap*r.h {
				segments = append(segments, cur)
				cur = nil
			}
			cur = append(cur, w)
		}
		if len(cur) > 0 {
			segments = append(segments, cur)
		}
	}
	rows := make([]Row, 0, len(segments))
	for _, ws := range segments {
		// Words that touch in the image belong to one token even when tesseract split them
		// ("sk-" "proj" "-AbC"); a real gap is a space.
		var b strings.Builder
		n := 0
		spans := make([]Span, len(ws))
		for i, w := range ws {
			if i > 0 && w.X0-ws[i-1].X1 >= 0.3*(w.Y1-w.Y0) {
				b.WriteByte(' ')
				n++
			}
			start := n
			b.WriteString(w.Text)
			n += utf8.RuneCountInString(w.Text)
			spans[i] = Span{start, n}
		}
		rows = append(rows, Row{Words: ws, Text: b.String(), Spans: spans})
	}
	return rows
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// ListLangs: the languages tesseract can use, e.g. [eng osd vie].
func ListLangs() ([]string, error) {
	out, err := exec.Command("tesseract", "--list-langs").CombinedOutput()
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("tesseract not found: %w", err)
	}
	var langs []string
	for i, l := range strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n") {
		if l = strings.TrimSpace(l); i > 0 && l != "" {
			langs = append(langs, l)
		}
	}
	return langs, nil
}

// AssertLangs fails unless every language of "vie+eng" is installed.
func AssertLangs(lang string) error {
	have, err := ListLangs()
	if err != nil {
		return err
	}
	var missing []string
	for _, l := range strings.Split(lang, "+") {
		found := false
		for _, h := range have {
			found = found || h == l
		}
		if !found {
			missing = append(missing, l)
		}
	}
	if len(missing) > 0 {
		installed := strings.Join(have, ", ")
		if installed == "" {
			installed = "none"
		}
		return fmt.Errorf("tesseract has no %q language data (installed: %s). Install it or choose another --lang", strings.Join(missing, ", "), installed)
	}
	return nil
}

// AssertWorks fails unless tesseract works for lang: every language installed and TSV output
// really produced (checked on a tiny blank image, ~0.1 s), so a broken setup stops at once.
func AssertWorks(lang string) error {
	if err := AssertLangs(lang); err != nil {
		return err
	}
	blank := bytes.Repeat([]byte{255}, 64*32)
	_, err := Image(PGM(blank, 64, 32), lang, 11)
	return err
}

// PGM wraps a grayscale frame as a PGM image, which tesseract reads from stdin.
func PGM(gray []byte, width, height int) []byte {
	head := fmt.Sprintf("P5\n%d %d\n255\n", width, height)
	return append([]byte(head), gray...)
}

// Image reads an image given as bytes (PNG or PGM, piped through stdin).
func Image(img []byte, lang string, psm int) ([]Word, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tesseract", "stdin", "stdout", "-l", lang, "--psm", strconv.Itoa(psm), "tsv")
	cmd.Env = append(os.Environ(), "OMP_THREAD_LIMIT=1")
	cmd.Stdin = bytes.NewReader(img)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		e := strings.TrimSpace(errb.String())
		if len(e) > 200 {
			e = e[len(e)-200:]
		}
		return nil, fmt.Errorf("tesseract: %v: %s", err, e)
	}
	return ParseTSV(out.String())
}
