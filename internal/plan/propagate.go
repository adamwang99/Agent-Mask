package plan

import "math"

// OCR misses text now and then (a pointer over it, a bad frame); pixels do not. Once a secret
// has been read in one frame, its exact image is looked for in every other frame the scan looked
// at: same columns, any height (so a scrolled line is found too). Where the image is, the secret
// is, whatever OCR said there. Comparing pixels is thousands of times cheaper than OCR.

// HalfSize: a half-size copy of a gray frame (2x2 average), kept per looked-at frame.
func HalfSize(gray []byte, w, h int) []byte {
	tw, th := w>>1, h>>1
	out := make([]byte, tw*th)
	for y := 0; y < th; y++ {
		for x := 0; x < tw; x++ {
			i := 2*y*w + 2*x
			out[y*tw+x] = byte((int(gray[i]) + int(gray[i+1]) + int(gray[i+w]) + int(gray[i+w+1]) + 2) >> 2)
		}
	}
	return out
}

type pxRect struct{ x0, y0, x1, y1 int }

func rectOf(b Box, tw, th int) pxRect {
	return pxRect{
		x0: max(0, int(math.Floor(b.X0*float64(tw)))), x1: min(tw, int(math.Ceil(b.X1*float64(tw)))),
		y0: max(0, int(math.Floor(b.Y0*float64(th)))), y1: min(th, int(math.Ceil(b.Y1*float64(th)))),
	}
}

// FindPatch: where (vertical offset in px) the patch `box` of ref appears in img; ok false when
// not found. A blank patch (no text in it) matches anything and is never searched.
func FindPatch(ref, img []byte, tw, th int, box Box) (dy int, ok bool) {
	const searchFrac, maxDiff, minContrast = 0.4, 6, 12
	r := rectOf(box, tw, th)
	if r.x1-r.x0 < 4 || r.y1-r.y0 < 2 {
		return 0, false
	}
	lo, hi := 255, 0
	for y := r.y0; y < r.y1; y++ {
		for x := r.x0; x < r.x1; x += 2 {
			v := int(ref[y*tw+x])
			lo, hi = min(lo, v), max(hi, v)
		}
	}
	if hi-lo < minContrast {
		return 0, false
	}
	span := int(math.Round(searchFrac * float64(th)))
	count := (r.y1 - r.y0) * ((r.x1 - r.x0 + 1) / 2)
	bestSum := maxDiff * count
	found := false
	// Nearest offsets first: the right answer is usually close, and it tightens the bound.
	for k := 0; k <= 2*span; k++ {
		d := -k / 2
		if k%2 == 1 {
			d = (k + 1) / 2
		}
		if r.y0+d < 0 || r.y1+d > th {
			continue
		}
		sum := 0
		for y := r.y0; y < r.y1 && sum < bestSum; y++ {
			a, b := y*tw, (y+d)*tw
			for x := r.x0; x < r.x1; x += 2 {
				v := int(ref[a+x]) - int(img[b+x])
				if v < 0 {
					v = -v
				}
				sum += v
			}
		}
		if sum < bestSum {
			bestSum, dy, found = sum, d, true
			if sum == 0 {
				break
			}
		}
	}
	return dy, found
}

// Keyframe is a frame the scan looked at: its half-size picture and the sample times that
// showed that same picture.
type Keyframe struct {
	Thumb []byte
	Times []float64
}

// Propagate adds points to tracks wherever their image is found in other keyframes.
func Propagate(tracks []*Track, keys []Keyframe, tw, th int, windowSec float64) {
	if windowSec == 0 {
		windowSec = 60
	}
	keyOfTime := map[float64]int{}
	for i, k := range keys {
		for _, t := range k.Times {
			keyOfTime[t] = i
		}
	}
	for _, tr := range tracks {
		seen := map[int]bool{}
		for _, p := range tr.Points {
			if k, ok := keyOfTime[p.T]; ok {
				seen[k] = true
			}
		}
		original := append([]Point(nil), tr.Points...)
		for ki, k := range keys {
			if seen[ki] {
				continue
			}
			t := k.Times[0]
			// Reference: the nearest frame where OCR did read this secret.
			ref := original[0]
			for _, p := range original {
				if math.Abs(p.T-t) < math.Abs(ref.T-t) {
					ref = p
				}
			}
			if math.Abs(ref.T-t) > windowSec {
				continue
			}
			refKey, ok := keyOfTime[ref.T]
			if !ok {
				continue
			}
			dy, found := FindPatch(keys[refKey].Thumb, k.Thumb, tw, th, ref.Box)
			if !found {
				continue
			}
			shift := float64(dy) / float64(th)
			box := Box{X0: ref.Box.X0, Y0: ref.Box.Y0 + shift, X1: ref.Box.X1, Y1: ref.Box.Y1 + shift}
			for _, time := range k.Times {
				tr.Points = append(tr.Points, Point{T: time, Box: box, Propagated: true})
			}
		}
	}
}

// BgAround: the background brightness of a box: the most common shade inside it (16 bins).
// Text is a minority of the pixels of a text box, so the commonest shade is the surface it is
// written on. Read inside, not around: around a line at a window's edge lies whatever is outside
// the window. Nil when the box is empty.
func BgAround(thumb []byte, tw, th int, box Box) *float64 {
	r := rectOf(box, tw, th)
	var bins [16]int
	var sums [16]float64
	for y := r.y0; y < r.y1; y++ {
		for x := r.x0; x < r.x1; x++ {
			v := thumb[y*tw+x]
			bins[v>>4]++
			sums[v>>4] += float64(v)
		}
	}
	best := -1
	for i := range bins {
		if bins[i] > 0 && (best < 0 || bins[i] > bins[best]) {
			best = i
		}
	}
	if best < 0 {
		return nil
	}
	v := math.Round(sums[best] / float64(bins[best]))
	return &v
}
