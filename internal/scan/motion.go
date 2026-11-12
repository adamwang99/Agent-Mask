package scan

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math"
	"os/exec"
	"sort"
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

const strips = 64 // a row's signature: the mean of 64 strips across it

// sig is a frame's signature: per row, the mean brightness of each strip (smoothed over the
// rows around it), and whether something is drawn there.
type sig struct {
	v   []float64
	ink []bool // the brightness changes within a few rows: a line of text, an edge
}

// signature of a gray frame. A stroke of screen text is about one row thick at this size:
// unsmoothed, a scroll of half a row changes a line's signature almost as much as another
// line would.
func signature(gray []byte, w, h int) *sig {
	raw := make([]float64, h*strips)
	for y := 0; y < h; y++ {
		row := gray[y*w : (y+1)*w]
		for k := 0; k < strips; k++ {
			x0, x1 := k*w/strips, (k+1)*w/strips
			s := 0
			for _, v := range row[x0:x1] {
				s += int(v)
			}
			raw[y*strips+k] = float64(s) / float64(max(1, x1-x0))
		}
	}
	at := func(v []float64, y, k int) float64 { return v[max(0, min(h-1, y))*strips+k] }
	s := &sig{v: make([]float64, h*strips), ink: make([]bool, h*strips)}
	for y := 0; y < h; y++ {
		for k := 0; k < strips; k++ {
			s.v[y*strips+k] = (at(raw, y-2, k) + 4*at(raw, y-1, k) + 6*at(raw, y, k) + 4*at(raw, y+1, k) + at(raw, y+2, k)) / 16
		}
	}
	for y := 0; y < h; y++ {
		for k := 0; k < strips; k++ {
			v := s.v[y*strips+k]
			s.ink[y*strips+k] = math.Abs(v-at(s.v, y-3, k)) > 2 || math.Abs(v-at(s.v, y+3, k)) > 2
		}
	}
	return s
}

// moving: which rows and strips of cur differ from ref where they stand (a fixed header,
// a sidebar or a toolbar stay put while the page scrolls, and must not vote).
func moving(cur, ref *sig, h int) (rows []bool, cols [strips]bool, count int) {
	rows = make([]bool, h)
	for y := 0; y < h; y++ {
		for k := 0; k < strips; k++ {
			if math.Abs(cur.v[y*strips+k]-ref.v[y*strips+k]) > 2 {
				rows[y] = true
				cols[k] = true
				count++
			}
		}
	}
	return
}

// cost of "cur is ref moved down by dy" (dy may lie between two rows: ref is then read between
// its rows) over the moving rows and strips; n is how many cells were compared. With inked,
// only cells where one of the two frames has something drawn: a page of text is mostly blank,
// and blank on blank fits at any offset.
func cost(cur, ref *sig, h int, dy float64, rows []bool, cols [strips]bool, inked bool) (float64, int) {
	whole := math.Floor(dy)
	frac := dy - whole
	sum, n := 0.0, 0
	for y := 0; y < h; y++ {
		if !rows[y] {
			continue
		}
		ry := y - int(whole) // rows ry and ry-1 of ref, the second weighing frac
		if ry < 0 || ry >= h || (frac > 0 && ry < 1) {
			continue
		}
		c, r := cur.v[y*strips:(y+1)*strips], ref.v[ry*strips:(ry+1)*strips]
		for k := 0; k < strips; k++ {
			if !cols[k] || (inked && !cur.ink[y*strips+k] && !ref.ink[ry*strips+k]) {
				continue
			}
			v := r[k]
			if frac > 0 {
				v = (1-frac)*v + frac*ref.v[(ry-1)*strips+k]
			}
			// Capped: lines entering at the edge (not in ref) cost a little, never everything.
			sum += math.Min(12, math.Abs(c[k]-v))
			n++
		}
	}
	if n == 0 {
		return math.Inf(1), 0
	}
	return sum / float64(n), n
}

// stayed: the share of what is drawn in cur that is the same in ref where it stands. When most
// of it is, the page has not scrolled, whatever else changed (a dialog, a box, a clip playing).
func stayed(cur, ref *sig) float64 {
	ink, same := 0, 0
	for i, drawn := range cur.ink {
		if drawn {
			ink++
			if math.Abs(cur.v[i]-ref.v[i]) <= 2 {
				same++
			}
		}
	}
	if ink == 0 {
		return 1
	}
	return float64(same) / float64(ink)
}

// fit is how well "cur is ref moved down by dy" holds.
type fit struct {
	dy    float64
	cost  float64 // of the fit at dy
	ok    bool    // a clear scroll by dy
	still bool    // the two pictures are the same but for an edit in place
	zero  float64 // the cost of "nothing moved"
}

const (
	probeStep = 9   // rows between the offsets a fit is compared with
	clearFit  = 0.5 // a scroll fits at most this share of the median fit around it
)

// match finds how far cur is ref moved (rows, sub-row precision), looking at guess-span to
// guess+span. The lines of a page look alike: a wrong offset (a line put on another line) can
// fit fairly well in absolute terms. So a fit counts as a scroll only when it is clearly better
// than the offsets around it, never on its cost alone: accepting a fair fit put the masks a
// line off for seconds. A wide span is looked at every third row first (the signature is smooth
// over more rows than that), then around each low point.
func match(cur, ref *sig, h, guess, span int) fit {
	rows, cols, count := moving(cur, ref, h)
	if count < len(cur.v)/100 { // under 1% of the picture changed: an edit in place, no scroll
		return fit{ok: true, still: true}
	}
	need := 0
	for y := 0; y < h; y++ {
		if !rows[y] {
			continue
		}
		for k := 0; k < strips; k++ {
			if cols[k] && cur.ink[y*strips+k] {
				need++
			}
		}
	}
	need = max(1, need*3/10) // at least 30% of what is drawn in the moving part must overlap
	res := fit{}
	res.zero, _ = cost(cur, ref, h, 0, rows, cols, false)
	costs := map[int]float64{}
	at := func(d int) (float64, bool) {
		c, ok := costs[d]
		if !ok {
			var n int
			if c, n = cost(cur, ref, h, float64(d), rows, cols, true); n < need {
				return 0, false
			}
			costs[d] = c
		}
		return c, true
	}
	best, bestDy := math.Inf(1), 0
	try := func(d int) {
		if c, ok := at(d); ok && c < best {
			best, bestDy = c, d
		}
	}
	lo, hi := max(-h+h/10, guess-span), min(h-h/10, guess+span)
	if hi-lo <= 80 {
		for d := lo; d <= hi; d++ {
			try(d)
		}
	} else {
		const stride = 3
		var coarse []int
		for d := lo; d <= hi; d += stride {
			try(d)
			coarse = append(coarse, d)
		}
		low := 2*best + 1
		for _, d := range coarse {
			c, ok := costs[d]
			if !ok || c > low {
				continue
			}
			if a, okA := costs[d-stride]; okA && a < c {
				continue
			}
			if b, okB := costs[d+stride]; okB && b < c {
				continue
			}
			for e := max(lo, d-stride+1); e <= min(hi, d+stride-1); e++ {
				try(e)
			}
		}
	}
	if math.IsInf(best, 1) {
		return res
	}
	// Between two rows: quarter rows around the best one, then the bottom of a parabola through
	// the best quarter and its neighbours.
	const q = 0.25
	fine := map[int]float64{0: best}
	bestQ := 0
	for k := -3; k <= 3; k++ {
		if k == 0 {
			continue
		}
		if c, n := cost(cur, ref, h, float64(bestDy)+float64(k)*q, rows, cols, true); n >= need {
			fine[k] = c
			if c < fine[bestQ] {
				bestQ = k
			}
		}
	}
	best = fine[bestQ]
	dy := float64(bestDy) + float64(bestQ)*q
	if a, okA := fine[bestQ-1]; okA {
		if b, okB := fine[bestQ+1]; okB {
			if den := a - 2*best + b; den > 1e-9 {
				dy += q * math.Max(-0.5, math.Min(0.5, 0.5*(a-b)/den))
			}
		}
	}
	// The offsets around: a few rows to a few lines away, both sides.
	var around []float64
	for k := 1; k <= 4; k++ {
		for _, d := range []int{bestDy - k*probeStep, bestDy + k*probeStep} {
			if c, ok := at(d); ok {
				around = append(around, c)
			}
		}
	}
	if len(around) < 2 {
		return res
	}
	sort.Float64s(around)
	if best > clearFit*around[len(around)/2] {
		return res
	}
	res.dy, res.cost, res.ok = dy, best, true
	return res
}

// MeasureMotion reads every frame at a small size and follows the scroll. Each frame is first
// placed from the frame before it (a short way, so no other line of the page can be taken for
// the right one), then against an anchor frame near where that puts it: measured against the
// anchor, slow scrolls (less than a pixel a frame at this size) add up instead of rounding
// away, and the small errors of frame-to-frame steps do not.
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
	var f follower
	gray := make([]byte, w*h)
	for {
		if _, err := io.ReadFull(r, gray); err != nil {
			break
		}
		s, scene := f.next(signature(gray, w, h), h)
		m.Shift = append(m.Shift, s)
		m.Scene = append(m.Scene, scene)
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("ffmpeg (motion): %v %s", err, strings.TrimSpace(lastChars(stderr.String(), 300)))
	}
	if len(m.Shift) == 0 {
		return nil, fmt.Errorf("no frame read for motion")
	}
	return m, nil
}

// follower follows the scroll frame after frame.
type follower struct {
	anchor, prev *sig
	anchorS, s   float64 // the page position of the anchor and of the last frame
	rel          float64 // rows the last frame is moved from the anchor
	vel          float64 // rows the last frame moved from the one before
	scene        int
}

// next: the page position (a share of the height) and the scene of the next frame.
func (f *follower) next(cur *sig, h int) (float64, int) {
	if f.prev == nil {
		f.anchor, f.prev = cur, cur
		return 0, 0
	}
	reanchor := func() { f.anchor, f.anchorS, f.rel = cur, f.s, 0 }
	// From the frame before: near the last step first. When nothing fits there, or the speed
	// changed, anywhere: the lines of a page look alike, and a jump of a line less a few rows
	// fits like a step of a few rows, only less well.
	step := match(cur, f.prev, h, int(math.Round(f.vel)), 12)
	if !step.still && (!step.ok || math.Abs(step.dy-f.vel) > 0.75) {
		if wide := match(cur, f.prev, h, 0, h); wide.ok && (!step.ok || wide.cost < step.cost) {
			step = wide
		}
	}
	switch {
	case step.ok:
		f.vel = step.dy
		at := f.rel + step.dy
		if a := match(cur, f.anchor, h, int(math.Round(at)), 3); a.ok {
			f.rel = a.dy
			f.s = f.anchorS - f.rel/float64(h)
		} else {
			// The anchor no longer shows (too far, or the page changed under it).
			f.s -= step.dy / float64(h)
			reanchor()
		}
	case step.zero <= 4.5:
		// Changed where it stands, nothing moved clearly: a pointer, a clip playing, typing.
		f.vel = 0
	default:
		f.vel = 0
		if a := match(cur, f.anchor, h, int(math.Round(f.rel)), h/6); a.ok {
			// One bad frame between two good ones.
			f.rel = a.dy
			f.s = f.anchorS - f.rel/float64(h)
		} else if stayed(cur, f.prev) < 0.5 {
			// Not a scroll: another window, another page. A new scene.
			f.scene++
			reanchor()
		}
		// Else most of the picture stays where it is: a dialog or a box over the same page.
	}
	if math.Abs(f.rel) > float64(h)/10 {
		reanchor()
	}
	f.prev = cur
	return f.s, f.scene
}

// Jumps: frames to read with OCR because the page has just jumped (more than three quarters of
// a screen a second): the first frame of each jump and the first frame after it. Where the page
// lands is known from the many steps of the jump, each a little off; a read there finds the
// text itself. skip are frames already read.
func (m *Motion) Jumps(skip map[int]bool) []int {
	limit := 0.75 / m.FPS
	var out []int
	jumping := false
	for f := 1; f < len(m.Shift); f++ {
		jump := m.Scene[f] == m.Scene[f-1] && math.Abs(m.Shift[f]-m.Shift[f-1]) > limit
		if jump != jumping && !skip[f] {
			out = append(out, f)
		}
		jumping = jump
	}
	return out
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
