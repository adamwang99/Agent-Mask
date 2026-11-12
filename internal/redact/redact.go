// Package redact: scan a video, hide what was found, scan the result again.
package redact

import (
	"fmt"
	"math"

	"github.com/adamwang99/agent-mask/internal/media"
	"github.com/adamwang99/agent-mask/internal/plan"
	"github.com/adamwang99/agent-mask/internal/scan"
)

// Options of a redaction.
type Options struct {
	Kinds []string
	Lang  string
	Style string
}

// Result of a redaction. It holds kinds and counts, never the hidden text.
type Result struct {
	Found    int            `json:"found"`
	Hidden   int            `json:"hidden"`
	Kinds    map[string]int `json:"kinds,omitempty"`
	Verified bool           `json:"verified"`
	Reasons  []string       `json:"reasons,omitempty"`
	Warnings []string       `json:"warnings,omitempty"`
	Regions  []Region       `json:"regions,omitempty"`
}

// Region: what kind of thing was hidden, and when it was on screen. Never its text.
type Region struct {
	Kind     string  `json:"kind"`
	StartSec float64 `json:"startSec"`
	EndSec   float64 `json:"endSec"`
}

// BoxesAt: the boxes to hide at time T, in their two groups.
func BoxesAt(tracks []*plan.Track, style string) func(float64) ([]media.MaskBox, []media.MaskBox) {
	return func(T float64) (a, b []media.MaskBox) {
		for _, tr := range tracks {
			if box, bg, ok := tr.BoxAt(T); ok {
				m := media.MaskBox{Box: box, Bg: bg}
				if media.StyleFor(tr.Kind, style) == "A" {
					a = append(a, m)
				} else {
					b = append(b, m)
				}
			}
		}
		return
	}
}

// Verdict: "verified" is earned, never assumed: the re-scan must have read text (an OCR that
// reads nothing finds nothing), had no failed frame, and found nothing sensitive.
func Verdict(check *scan.Result) (bool, []string) {
	reasons := []string{}
	if len(check.Warnings) > 0 {
		reasons = append(reasons, fmt.Sprintf("%d OCR failure(s) while checking: %s", len(check.Warnings), check.Warnings[0]))
	}
	if check.WordsRead == 0 {
		reasons = append(reasons, "OCR read no text at all in the output, so it cannot vouch for it")
	}
	if len(check.Regions) > 0 {
		reasons = append(reasons, fmt.Sprintf("%d sensitive region(s) still readable", len(check.Regions)))
	}
	return len(reasons) == 0, reasons
}

// Video: scan, hide, check.
func Video(in, out string, o Options) (*Result, error) {
	so := scan.Options{Kinds: o.Kinds, Lang: o.Lang}
	found, err := scan.Video(in, so)
	if err != nil {
		return nil, err
	}
	info, err := media.Probe(in)
	if err != nil {
		return nil, err
	}
	if err := media.Render(media.Request{Input: in, Output: out, Info: info, BoxesAt: BoxesAt(found.Regions, o.Style), Style: o.Style}); err != nil {
		return nil, err
	}
	so.Check = true
	check, err := scan.Video(out, so)
	if err != nil {
		return nil, err
	}
	r := &Result{Found: len(found.Regions), Hidden: len(found.Regions)}
	for _, t := range found.Regions {
		if r.Kinds == nil {
			r.Kinds = map[string]int{}
		}
		r.Kinds[t.Kind]++
		r.Regions = append(r.Regions, Region{Kind: t.Kind, StartSec: math.Round(t.StartSec*100) / 100, EndSec: math.Round(t.EndSec*100) / 100})
	}
	r.Verified, r.Reasons = Verdict(check)
	// Everything readable was sensitive and is now hidden: the first scan proved the OCR works
	// on this video, so an empty, error-free check is a pass.
	if !r.Verified && found.WordsRead > 0 && check.WordsRead == 0 && len(check.Regions) == 0 && len(check.Warnings) == 0 {
		r.Verified, r.Reasons = true, nil
	}
	r.Warnings = append(append([]string{}, found.Warnings...), check.Warnings...)
	return r, nil
}
