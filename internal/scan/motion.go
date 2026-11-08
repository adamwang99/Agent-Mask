package scan

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math"
	"os/exec"
	"strings"
)

// Motion is how the content scrolls, measured on every frame (not just the frames read by
// OCR): screen recordings scroll, and a secret moves with the page between two reads. Pixels
// are compared, never read, so this costs little.
type Motion struct {
	FPS   float64
	Shift []float64 // per frame: how far the page has scrolled, as a share of the height
	Scene []int     // per frame: a new number where the picture changed beyond a scroll
}

// FrameAt: the frame shown at time t.
func (m *Motion) FrameAt(t float64) int {
	f := int(math.Round(t * m.FPS))
	return max(0, min(len(m.Shift)-1, f))
}

const strips = 32 // a row's signature: the mean of 32 strips across it

// signature: per row, the mean brightness of each strip.
func signature(gray []byte, w, h int) []float64 {
	sig := make([]float64, h*strips)
	for y := 0; y < h; y++ {
		row := gray[y*w : (y+1)*w]
		for k := 0; k < strips; k++ {
			x0, x1 := k*w/strips, (k+1)*w/strips
			s := 0
			for _, v := range row[x0:x1] {
				s += int(v)
			}
			sig[y*strips+k] = float64(s) / float64(max(1, x1-x0))
		}
	}
	return sig
}

// moving: which rows and strips of cur differ from ref where they stand (a fixed header,
// a sidebar or a toolbar stay put while the page scrolls, and must not vote).
func moving(cur, ref []float64, h int) (rows []bool, cols [strips]bool, count int) {
	rows = make([]bool, h)
	for y := 0; y < h; y++ {
		for k := 0; k < strips; k++ {
			if math.Abs(cur[y*strips+k]-ref[y*strips+k]) > 2 {
				rows[y] = true
				cols[k] = true
				count++
			}
		}
	}
	return
}

// cost of "cur is ref moved down by dy" over the moving rows and strips; n is how many cells
// overlapped.
func cost(cur, ref []float64, h, dy int, rows []bool, cols [strips]bool) (float64, int) {
	sum, n := 0.0, 0
	for y := 0; y < h; y++ {
		if !rows[y] {
			continue
		}
		ry := y - dy
		if ry < 0 || ry >= h {
			continue
		}
		for k := 0; k < strips; k++ {
			if cols[k] {
				// Capped: lines entering at the edge (not in ref) cost a little, never everything.
				sum += math.Min(12, math.Abs(cur[y*strips+k]-ref[ry*strips+k]))
				n++
			}
		}
	}
	if n == 0 {
		return math.Inf(1), 0
	}
	return sum / float64(n), n
}

// match finds how far cur is ref moved (rows, sub-row precision); ok when it is a clear scroll.
func match(cur, ref []float64, h, guess, span int) (dy float64, ok, still bool) {
	rows, cols, count := moving(cur, ref, h)
	if count < len(cur)/100 { // under 1% of the picture changed: an edit in place, no scroll
		return 0, true, true
	}
	need := 0
	for _, r := range rows {
		if r {
			need++
		}
	}
	nc := 0
	for _, c := range cols {
		if c {
			nc++
		}
	}
	need = need * nc * 3 / 10 // at least 30% of the moving cells must overlap
	best, bestDy := math.Inf(1), 0
	costs := map[int]float64{}
	lo, hi := max(-h+h/10, guess-span), min(h-h/10, guess+span)
	for d := lo; d <= hi; d++ {
		c, n := cost(cur, ref, h, d, rows, cols)
		if n < need {
			continue
		}
		costs[d] = c
		if c < best {
			best, bestDy = c, d
		}
	}
	zero, _ := cost(cur, ref, h, 0, rows, cols)
	// A good fit anywhere is a scroll (a slow one fits about as well at 0 and 1 row: the
	// sub-row step below settles it). No good fit at all is not a scroll.
	if math.IsInf(best, 1) || best > 4.5 {
		return 0, false, false
	}
	_ = zero
	dy = float64(bestDy)
	// Sub-row precision: the bottom of a parabola through the best cost and its neighbours.
	if a, okA := costs[bestDy-1]; okA {
		if b, okB := costs[bestDy+1]; okB {
			if den := a - 2*best + b; den > 1e-9 {
				dy += math.Max(-0.5, math.Min(0.5, 0.5*(a-b)/den))
			}
		}
	}
	return dy, true, false
}

// MeasureMotion reads every frame at a small size and follows the scroll. The page position is
// measured against an anchor frame, not frame to frame, so slow scrolls (less than a pixel a
// frame at this size) add up instead of rounding away.
func MeasureMotion(video, cropFilter string, srcW, srcH int, fps float64) (*Motion, error) {
	if fps <= 0 || fps > 120 {
		fps = 30
	}
	w := min(960, srcW)
	h := int(math.Round(float64(srcH) * float64(w) / float64(srcW)))
	h -= h % 2
	cmd := exec.Command("ffmpeg", "-v", "error", "-i", video,
		"-vf", fmt.Sprintf("%ssetpts=PTS-STARTPTS,fps=%g,scale=%d:%d:flags=area,format=gray", cropFilter, fps, w, h),
		"-f", "rawvideo", "-pix_fmt", "gray", "-")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	m := &Motion{FPS: fps}
	r := bufio.NewReaderSize(out, 1<<20)
	var anchor, prev []float64
	var anchorS, prevS, lastRel float64
	scene := 0
	gray := make([]byte, w*h)
	for {
		if _, err := io.ReadFull(r, gray); err != nil {
			break
		}
		sig := signature(gray, w, h)
		s := prevS
		if anchor == nil {
			anchor, anchorS, lastRel = sig, 0, 0
		} else {
			dy, ok, _ := match(sig, anchor, h, int(math.Round(lastRel)), h/6)
			if !ok {
				dy, ok, _ = match(sig, anchor, h, 0, h)
			}
			if ok {
				s = anchorS - dy/float64(h)
				lastRel = dy
			} else if dy2, ok2, _ := match(sig, prev, h, 0, h); ok2 {
				// Too far from the anchor (a jump): follow from the previous frame.
				s = prevS - dy2/float64(h)
				anchor, anchorS, lastRel = sig, s, 0
			} else {
				// Not a scroll: another window, another page. A new scene.
				scene++
				anchor, anchorS, lastRel = sig, s, 0
			}
			if math.Abs(lastRel) > float64(h)/10 {
				anchor, anchorS, lastRel = sig, s, 0
			}
		}
		m.Shift = append(m.Shift, s)
		m.Scene = append(m.Scene, scene)
		prev, prevS = sig, s
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("ffmpeg (motion): %v %s", err, strings.TrimSpace(lastChars(stderr.String(), 300)))
	}
	if len(m.Shift) == 0 {
		return nil, fmt.Errorf("no frame read for motion")
	}
	return m, nil
}

// Uncovered: frames to read with OCR because they show a part of the page that no read frame
// showed (a fast scroll or a jump can pass whole screens of text between two reads). read are
// the times already read.
func (m *Motion) Uncovered(read []float64) []int {
	type span struct{ a, b float64 }
	covered := map[int][]span{}
	for _, t := range read {
		f := m.FrameAt(t)
		covered[m.Scene[f]] = append(covered[m.Scene[f]], span{m.Shift[f], m.Shift[f] + 1})
	}
	missing := func(sc int, a, b float64) float64 {
		// The longest piece of [a,b] that no read frame covered.
		cut := []span{{a, b}}
		for _, c := range covered[sc] {
			var next []span
			for _, p := range cut {
				if c.b <= p.a || c.a >= p.b {
					next = append(next, p)
					continue
				}
				if c.a > p.a {
					next = append(next, span{p.a, c.a})
				}
				if c.b < p.b {
					next = append(next, span{c.b, p.b})
				}
			}
			cut = next
		}
		longest := 0.0
		for _, p := range cut {
			longest = math.Max(longest, p.b-p.a)
		}
		return longest
	}
	var out []int
	for f := range m.Shift {
		sc := m.Scene[f]
		if missing(sc, m.Shift[f], m.Shift[f]+1) > 0.06 {
			out = append(out, f)
			covered[sc] = append(covered[sc], span{m.Shift[f], m.Shift[f] + 1})
		}
	}
	return out
}
