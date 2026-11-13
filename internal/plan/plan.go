// Package plan turns OCR rows into moving masks. Each OCR'd frame yields boxes; boxes are linked
// across frames into tracks (one per piece of sensitive text), and a track answers "where is it
// at time T", so the mask follows text that scrolls instead of guarding where it used to be.
package plan

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/adamwang99/agent-mask/internal/detect"
	"github.com/adamwang99/agent-mask/internal/ocr"
)

// Box is normalized 0..1.
type Box struct {
	X0 float64 `json:"x0"`
	Y0 float64 `json:"y0"`
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
}

// Detection is one sensitive text found in one frame.
type Detection struct {
	Kind string
	Text string
	Box  Box
}

// Secrets are usually the last thing on a line and OCR may split them into pieces, so the box
// runs to the end of the row segment: hiding a little extra beats leaking the tail.
var toEndOfRow = map[string]bool{"api-key": true, "secret-assignment": true, "high-entropy": true, "jwt": true, "url-credentials": true}

// DetectInRows finds sensitive text in OCR rows of a width x height frame.
func DetectInRows(rows []ocr.Row, width, height float64, kinds []string) []Detection {
	var out []Detection
	for _, row := range rows {
		text := []rune(row.Text)
		for _, m := range detect.Find(detect.FoldOCR(row.Text), kinds) {
			first := -1
			for i, sp := range row.Spans {
				if sp.Start < m.End && sp.End > m.Start {
					first = i
					break
				}
			}
			if first < 0 {
				continue
			}
			var hit []ocr.Word
			for i, sp := range row.Spans {
				if (toEndOfRow[m.Kind] && i >= first) || (sp.Start < m.End && sp.End > m.Start) {
					hit = append(hit, row.Words[i])
				}
			}
			x0, y0, x1, y1, h := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1), 0.0
			for _, w := range hit {
				x0, y0 = math.Min(x0, w.X0), math.Min(y0, w.Y0)
				x1, y1 = math.Max(x1, w.X1), math.Max(y1, w.Y1)
				h = math.Max(h, w.Y1-w.Y0)
			}
			// Margins scale with the text height: a fixed 2 px is nothing on a 4K screen.
			right := 0.25 * h
			if toEndOfRow[m.Kind] {
				right = h
			}
			out = append(out, Detection{
				Kind: m.Kind,
				Text: string(text[m.Start:m.End]),
				Box:  clampBox(Box{X0: (x0 - 0.25*h) / width, Y0: (y0 - 0.15*h) / height, X1: (x1 + right) / width, Y1: (y1 + 0.15*h) / height}),
			})
		}
	}
	return out
}

func clamp01(v float64) float64 { return math.Min(1, math.Max(0, v)) }

func clampBox(b Box) Box {
	return Box{X0: clamp01(b.X0), Y0: clamp01(b.Y0), X1: clamp01(b.X1), Y1: clamp01(b.Y1)}
}

// Union of two boxes.
func Union(a, b Box) Box {
	return Box{X0: math.Min(a.X0, b.X0), Y0: math.Min(a.Y0, b.Y0), X1: math.Max(a.X1, b.X1), Y1: math.Max(a.Y1, b.Y1)}
}

// OverlapRatio: how much two boxes sit on the same line: vertical intersection over vertical
// union, 0 when they do not overlap horizontally. Over the union so a tall box (two lines OCR
// glued) does not swallow the line next to it.
func OverlapRatio(a, b Box) float64 {
	if ix := math.Min(a.X1, b.X1) - math.Max(a.X0, b.X0); ix <= 0 {
		return 0
	}
	iy := math.Min(a.Y1, b.Y1) - math.Max(a.Y0, b.Y0)
	uy := math.Max(a.Y1, b.Y1) - math.Min(a.Y0, b.Y0)
	if iy > 0 && uy > 0 {
		return iy / uy
	}
	return 0
}

// Norm folds look-alikes OCR flips between frames.
func Norm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '@' || r == '.') {
			continue
		}
		switch r {
		case 'o':
			r = '0'
		case 'i', 'l':
			r = '1'
		case 's':
			r = '5'
		case 'g':
			r = '@'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Similarity 0..1 from the edit distance of the folded strings.
func Similarity(a, b string) float64 {
	s, t := Norm(a), Norm(b)
	if len(s) == 0 && len(t) == 0 {
		return 1
	}
	prev := make([]int, len(t)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(s); i++ {
		diag := prev[0]
		prev[0] = i
		for j := 1; j <= len(t); j++ {
			tmp := prev[j]
			cost := 1
			if s[i-1] == t[j-1] {
				cost = 0
			}
			prev[j] = min(prev[j]+1, prev[j-1]+1, diag+cost)
			diag = tmp
		}
	}
	return 1 - float64(prev[len(t)])/float64(max(len(s), len(t)))
}

var severity = []string{"phone", "email", "ip-address", "high-entropy", "credit-card", "url-credentials", "secret-assignment", "jwt", "api-key", "private-key", "mask"}

func rank(k string) int {
	for i, s := range severity {
		if s == k {
			return i
		}
	}
	return -1
}

var secretFamily = map[string]bool{"api-key": true, "secret-assignment": true, "high-entropy": true, "jwt": true, "url-credentials": true, "private-key": true}

func sameFamily(a, b string) bool { return a == b || (secretFamily[a] && secretFamily[b]) }

// Point is where a track was at one look.
type Point struct {
	T   float64  `json:"t"`
	Box Box      `json:"box"`
	Bg  *float64 `json:"bg,omitempty"` // gray level of the surface under the text, when known
	// Propagated: found by its pixels in a frame where OCR missed it.
	Propagated bool `json:"propagated,omitempty"`
	// Off: scrolled out of view at this moment (nothing to hide).
	Off bool `json:"off,omitempty"`
}

// Track is one piece of sensitive text over time.
type Track struct {
	ID       string  `json:"id"`
	Kind     string  `json:"kind"`
	Preview  string  `json:"preview"`
	Text     string  `json:"-"`
	Points   []Point `json:"points"`
	StartSec float64 `json:"startSec"` // first on screen
	EndSec   float64 `json:"endSec"`   // last on screen
	OnScreen []Span  `json:"onScreen"` // each stretch on screen: a page scrolls away and comes back
	Box      Box     `json:"box"`
	// Sightings: the frames where OCR itself read the text, with where it read it. The other
	// points are worked out from these (pixels, scroll); a reviewer checks these first.
	Sightings []Sighting `json:"sightings,omitempty"`
}

// Sighting is one OCR read of a track: second of the video, and box on that frame.
type Sighting struct {
	T   float64 `json:"t"`
	Box Box     `json:"box"`
}

// Span is a stretch of time.
type Span struct {
	StartSec float64 `json:"startSec"`
	EndSec   float64 `json:"endSec"`
}

// Sample is every detection of one OCR'd frame.
type Sample struct {
	TimeSec    float64
	Detections []Detection
}

// matchScore: the same thing seen again: same line, or the same text moved (scrolled) on the
// same column.
func matchScore(tr *Track, d Detection) float64 {
	if !sameFamily(tr.Kind, d.Kind) {
		return 0
	}
	last := tr.Points[len(tr.Points)-1]
	if line := OverlapRatio(last.Box, d.Box); line >= 0.5 {
		return 1 + line
	}
	sim := Similarity(tr.Text, d.Text)
	if xo := math.Min(last.Box.X1, d.Box.X1) - math.Max(last.Box.X0, d.Box.X0); sim >= 0.6 && xo > 0 {
		return sim
	}
	return 0
}

// LinkTracks links per-frame detections into tracks (points not yet sorted, no spans).
func LinkTracks(samples []Sample, maxBridgeSec float64) []*Track {
	if maxBridgeSec == 0 {
		maxBridgeSec = 5
	}
	ordered := append([]Sample(nil), samples...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].TimeSec < ordered[j].TimeSec })
	var tracks []*Track
	for _, sample := range ordered {
		var open []*Track
		for _, tr := range tracks {
			if sample.TimeSec-tr.Points[len(tr.Points)-1].T <= maxBridgeSec {
				open = append(open, tr)
			}
		}
		// Best pairs first, so two neighbouring lines do not swap tracks.
		type pair struct {
			di    int
			tr    *Track
			score float64
		}
		var pairs []pair
		for di, d := range sample.Detections {
			for _, tr := range open {
				if s := matchScore(tr, d); s > 0 {
					pairs = append(pairs, pair{di, tr, s})
				}
			}
		}
		sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].score > pairs[j].score })
		used, taken := map[int]bool{}, map[*Track]bool{}
		for _, p := range pairs {
			if used[p.di] || taken[p.tr] {
				continue
			}
			d := sample.Detections[p.di]
			last := &p.tr.Points[len(p.tr.Points)-1]
			if last.T == sample.TimeSec {
				last.Box = Union(last.Box, d.Box)
			} else {
				p.tr.Points = append(p.tr.Points, Point{T: sample.TimeSec, Box: d.Box})
			}
			if rank(d.Kind) > rank(p.tr.Kind) {
				p.tr.Kind = d.Kind
			}
			used[p.di], taken[p.tr] = true, true
		}
		for di, d := range sample.Detections {
			if used[di] {
				continue
			}
			// Two passes of OCR on one frame may report the same thing twice.
			var dup *Track
			for _, tr := range tracks {
				last := tr.Points[len(tr.Points)-1]
				if last.T == sample.TimeSec && sameFamily(tr.Kind, d.Kind) && OverlapRatio(last.Box, d.Box) >= 0.5 {
					dup = tr
					break
				}
			}
			if dup != nil {
				last := &dup.Points[len(dup.Points)-1]
				last.Box = Union(last.Box, d.Box)
				continue
			}
			tracks = append(tracks, &Track{Kind: d.Kind, Text: d.Text, Points: []Point{{T: sample.TimeSec, Box: d.Box}}})
		}
	}
	return tracks
}

// FinalizeTracks sorts points, sets each track's stretches on screen and bounding box, numbers
// the tracks. A stretch runs between the points where the text is out of view (a track that
// follows the scroll has a point on every frame). Where no point says it is out of view, the
// stretch extends to the previous / next look at the screen: the text may have appeared just
// after the previous look and gone just before the next.
func FinalizeTracks(tracks []*Track, times []float64, durationSec float64) []*Track {
	sorted := append([]float64(nil), times...)
	sort.Float64s(sorted)
	prevTime := func(t float64) float64 {
		p := 0.0
		for _, x := range sorted {
			if x < t {
				p = x
			}
		}
		return p
	}
	nextTime := func(t float64) float64 {
		for _, x := range sorted {
			if x > t {
				return x
			}
		}
		return durationSec
	}
	out := make([]*Track, 0, len(tracks))
	for _, tr := range tracks {
		pts := append([]Point(nil), tr.Points...)
		sort.SliceStable(pts, func(i, j int) bool { return pts[i].T < pts[j].T })
		var box Box
		var seen []Sighting
		first := true
		for _, p := range pts {
			if p.Off {
				continue
			}
			if !p.Propagated {
				seen = append(seen, Sighting{T: p.T, Box: p.Box})
			}
			if first {
				box, first = p.Box, false
			} else {
				box = Union(box, p.Box)
			}
		}
		preview := tr.Preview
		if preview == "" {
			preview = detect.MaskedPreview(tr.Text)
		}
		var spans []Span
		for i := 0; i < len(pts); i++ {
			if pts[i].Off {
				continue
			}
			j := i
			for j+1 < len(pts) && !pts[j+1].Off {
				j++
			}
			sp := Span{StartSec: pts[i].T, EndSec: pts[j].T}
			if i == 0 {
				sp.StartSec = prevTime(pts[0].T)
			}
			if j == len(pts)-1 {
				sp.EndSec = math.Min(durationSec, nextTime(pts[j].T))
			}
			spans = append(spans, sp)
			i = j
		}
		if len(spans) == 0 { // never in view: nothing to hide
			spans = []Span{{StartSec: pts[0].T, EndSec: pts[0].T}}
		}
		out = append(out, &Track{Kind: tr.Kind, Preview: preview, Text: tr.Text, Points: pts,
			StartSec: spans[0].StartSec, EndSec: spans[len(spans)-1].EndSec, OnScreen: spans, Box: box, Sightings: seen})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartSec < out[j].StartSec })
	for i, tr := range out {
		tr.ID = fmt.Sprintf("r%d", i+1)
	}
	return out
}

// BuildTracks links and finalizes.
func BuildTracks(samples []Sample, durationSec, maxBridgeSec float64) []*Track {
	times := make([]float64, len(samples))
	for i, s := range samples {
		times[i] = s.TimeSec
	}
	return FinalizeTracks(LinkTracks(samples, maxBridgeSec), times, durationSec)
}

func lerp(a, b, f float64) float64 { return a + (b-a)*f }

// BoxAt is where a track is at time T, with the gray level of the surface under it (nil when
// unknown); ok is false when it is not on screen. Between two looks close in time (≤ 0.5 s:
// dense sampling of a scroll) the mask covers both positions and all between; across a longer
// gap it moves linearly, plus the nearer look's box, so a slow scroll is never under-covered.
func (tr *Track) BoxAt(T float64) (box Box, bg *float64, ok bool) {
	if T < tr.StartSec || T > tr.EndSec {
		return Box{}, nil, false
	}
	pts := tr.Points
	if T <= pts[0].T {
		return pts[0].Box, pts[0].Bg, !pts[0].Off
	}
	last := pts[len(pts)-1]
	if T >= last.T {
		return last.Box, last.Bg, !last.Off
	}
	i := sort.Search(len(pts), func(k int) bool { return pts[k].T >= T }) - 1
	a, b := pts[i], pts[i+1]
	switch {
	case a.Off && b.Off:
		return Box{}, nil, false
	case a.Off:
		return b.Box, b.Bg, true
	case b.Off:
		return a.Box, a.Bg, true
	}
	near := a
	if T-a.T > b.T-T {
		near = b
	}
	if b.T-a.T <= 0.5 {
		return Union(a.Box, b.Box), near.Bg, true
	}
	f := (T - a.T) / (b.T - a.T)
	mid := Box{X0: lerp(a.Box.X0, b.Box.X0, f), Y0: lerp(a.Box.Y0, b.Box.Y0, f), X1: lerp(a.Box.X1, b.Box.X1, f), Y1: lerp(a.Box.Y1, b.Box.Y1, f)}
	return Union(mid, near.Box), near.Bg, true
}

// FollowScroll moves each secret with the page on every frame. shift is how far the page has
// scrolled at each frame (a share of the height), scene where the picture changed beyond a
// scroll. A secret read at a few frames sits at one place on the page: from there its box is
// known on every frame of the scene, including the frames between two reads, a jump, and the
// moment it enters or leaves at an edge. The same text can be at several places on one page
// (an email in a header and again further down): each place becomes its own track, so none of
// them borrows another's position. Secrets that stay put while the page scrolls (in a header
// or a sidebar) keep their reads as they are.
func FollowScroll(tracks []*Track, shift []float64, scene []int, fps float64) []*Track {
	frameAt := func(t float64) int { return max(0, min(len(shift)-1, int(math.Round(t*fps)))) }
	type read struct {
		p      Point
		s, py0 float64
	}
	var out []*Track
	for _, tr := range tracks {
		byScene := map[int][]read{}
		var scenes []int
		for _, p := range tr.Points {
			if p.Off {
				continue
			}
			f := frameAt(p.T)
			sc := scene[f]
			if _, ok := byScene[sc]; !ok {
				scenes = append(scenes, sc)
			}
			byScene[sc] = append(byScene[sc], read{p, shift[f], p.Box.Y0 + shift[f]})
		}
		sort.Ints(scenes)
		for _, sc := range scenes {
			reads := byScene[sc]
			sort.SliceStable(reads, func(i, j int) bool { return reads[i].py0 < reads[j].py0 })
			// Places on the page: reads within 0.02 of the height of each other.
			var places [][]read
			for i, r := range reads {
				if i == 0 || r.py0-reads[i-1].py0 > 0.02 {
					places = append(places, nil)
				}
				places[len(places)-1] = append(places[len(places)-1], r)
			}
			for _, place := range places {
				nt := &Track{Kind: tr.Kind, Text: tr.Text, Preview: tr.Preview}
				var pageY0, pageY1, screenY []float64
				minS, maxS := math.Inf(1), math.Inf(-1)
				box := place[0].p.Box
				for _, r := range place {
					nt.Points = append(nt.Points, r.p)
					pageY0 = append(pageY0, r.py0)
					pageY1 = append(pageY1, r.p.Box.Y1+r.s)
					screenY = append(screenY, r.p.Box.Y0)
					minS, maxS = math.Min(minS, r.s), math.Max(maxS, r.s)
					box.X0, box.X1 = math.Min(box.X0, r.p.Box.X0), math.Max(box.X1, r.p.Box.X1)
				}
				out = append(out, nt)
				if maxS-minS > 0.02 && spread(screenY) < 0.005 {
					continue // it did not move while the page did: a fixed part of the screen
				}
				y0, y1 := median(pageY0), median(pageY1)
				pad := 0.004 + 0.1*(y1-y0)
				bg := place[0].p.Bg
				for f := range shift {
					if scene[f] != sc {
						continue
					}
					T := float64(f) / fps
					top, bottom := y0-shift[f]-pad, y1-shift[f]+pad
					if bottom < 0 || top > 1 {
						nt.Points = append(nt.Points, Point{T: T, Off: true, Propagated: true})
						continue
					}
					b := Box{X0: box.X0, Y0: math.Max(0, top), X1: box.X1, Y1: math.Min(1, bottom)}
					nt.Points = append(nt.Points, Point{T: T, Box: b, Bg: bg, Propagated: true})
				}
				sort.SliceStable(nt.Points, func(i, j int) bool { return nt.Points[i].T < nt.Points[j].T })
			}
		}
	}
	return out
}

func spread(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	lo, hi := v[0], v[0]
	for _, x := range v {
		lo, hi = math.Min(lo, x), math.Max(hi, x)
	}
	return hi - lo
}

func median(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)/2]
}
