// Package scan finds the sensitive text of a video, as tracks. Light by design:
//   - frames come from ffmpeg as raw grayscale, straight into memory: no screenshot on disk;
//   - the screen is looked at LookFPS times a second; a look at an unchanged screen repeats the
//     last result for free, a look at a changed one is OCR'd. Still screens cost nothing and a
//     scroll is followed look by look;
//   - two OCR passes (psm 11 sparse text, psm 3 page layout) misread different characters, so
//     their detections are merged;
//   - at most half the cores.
package scan

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/adamwang99/agent-mask/internal/media"
	"github.com/adamwang99/agent-mask/internal/ocr"
	"github.com/adamwang99/agent-mask/internal/plan"
)

// BlockMeans: the mean brightness of each 16x16 block. Comparing block means rather than pixels
// ignores per-pixel noise (compression, and our own noise masks when verifying an output),
// while text appearing, vanishing or moving shifts the mean of the blocks it covers.
func BlockMeans(gray []byte, w, h int) []float64 {
	const size = 16
	bw, bh := (w+size-1)/size, (h+size-1)/size
	sums := make([]float64, bw*bh)
	counts := make([]int, bw*bh)
	for y := 0; y < h; y++ {
		row := (y / size) * bw
		for x := 0; x < w; x++ {
			b := row + x/size
			sums[b] += float64(gray[y*w+x])
			counts[b]++
		}
	}
	for i := range sums {
		sums[i] /= float64(counts[i])
	}
	return sums
}

// ChangedBlocks: how many blocks moved by more than 6 levels.
func ChangedBlocks(a, b []float64) int {
	n := 0
	for i := range a {
		if math.Abs(a[i]-b[i]) > 6 {
			n++
		}
	}
	return n
}

// Upscale2x of a gray frame (bilinear). Screen text at 1080p is ~18 px tall, under what
// tesseract reads reliably (~30 px): on a real recording 1x missed a whole API_KEY line.
func Upscale2x(src []byte, w, h int) []byte {
	W := 2 * w
	out := make([]byte, W*2*h)
	for y := 0; y < h; y++ {
		y1 := min(y+1, h-1)
		for x := 0; x < w; x++ {
			x1 := min(x+1, w-1)
			a, b, c, d := int(src[y*w+x]), int(src[y*w+x1]), int(src[y1*w+x]), int(src[y1*w+x1])
			o := 2*y*W + 2*x
			out[o] = byte(a)
			out[o+1] = byte((a + b + 1) >> 1)
			out[o+W] = byte((a + c + 1) >> 1)
			out[o+W+1] = byte((a + b + c + d + 2) >> 2)
		}
	}
	return out
}

// OCRSize: the size of the OCR stream: at most maxWidth wide, both sides even.
func OCRSize(width, height int) (int, int) {
	w := min(2560, width)
	w -= w % 2
	h := 2 * int(math.Round(float64(height)*float64(w)/float64(width)/2))
	return w, h
}

// Crop is a pixel rectangle of the picture.
type Crop struct{ X, Y, W, H int }

// Options of a scan.
type Options struct {
	LookFPS     float64  // default 3
	Kinds       []string // nil: all default kinds
	Lang        string   // tesseract languages, default "eng"
	PSMs        []int    // default 11 and 3
	Concurrency int      // default half the cores
	BigChange   float64  // share of blocks that makes a change "big", default 0.004
	RefreshSec  float64  // any change is read within this, default 1
	Crop        *Crop    // scan only this part (the window of a framed video)
	// Check: this scan checks a masked output. It looks at the frames halfway between the
	// frames a first scan looks at, so it tests the masks where they were worked out from the
	// scroll, not where the text was read: a check on the same frames would only repeat what
	// the first scan saw.
	Check bool
}

// Result of a scan.
type Result struct {
	Video         string        `json:"video"`
	Width         int           `json:"width"`
	Height        int           `json:"height"`
	DurationSec   float64       `json:"durationSec"`
	LookFPS       float64       `json:"lookFps"`
	FramesScanned int           `json:"framesScanned"`
	OCRRuns       int           `json:"ocrRuns"`
	WordsRead     int           `json:"wordsRead"`
	Warnings      []string      `json:"warnings,omitempty"`
	Regions       []*plan.Track `json:"regions"`
}

// Video scans a video.
func Video(video string, o Options) (*Result, error) {
	if o.LookFPS == 0 {
		o.LookFPS = 3
	}
	if o.Lang == "" {
		o.Lang = "eng"
	}
	if o.PSMs == nil {
		o.PSMs = []int{11, 3}
	}
	if o.Concurrency == 0 {
		o.Concurrency = max(1, runtime.NumCPU()/2)
	}
	if o.BigChange == 0 {
		o.BigChange = 0.004
	}
	if o.RefreshSec == 0 {
		o.RefreshSec = 1
	}
	if err := ocr.AssertWorks(o.Lang); err != nil {
		return nil, err
	}
	info, err := media.Probe(video)
	if err != nil {
		return nil, err
	}
	srcW, srcH := info.Width, info.Height
	cropFilter := ""
	if o.Crop != nil {
		srcW, srcH = o.Crop.W, o.Crop.H
		cropFilter = fmt.Sprintf("crop=%d:%d:%d:%d,", o.Crop.W, o.Crop.H, o.Crop.X, o.Crop.Y)
	}
	w, h := OCRSize(srcW, srcH)
	zoom := 1
	if w <= 1920 { // small screens are read at twice the size
		zoom = 2
	}
	// Every k-th frame, chosen by number: the fps filter would take a frame up to half a look
	// late, and a fast scroll then puts the text where it was not at that time. The same frame
	// numbering (start at 0, constant rate) is used by the motion pass and by the render.
	vfps := info.FPS
	if vfps <= 0 || vfps > 120 {
		vfps = 30
	}
	every := max(1, int(math.Round(vfps/o.LookFPS)))
	phase := 0
	if o.Check {
		phase = every / 2
	}
	cmd := exec.Command("ffmpeg", "-v", "error", "-i", video,
		"-vf", fmt.Sprintf("%ssetpts=PTS-STARTPTS,fps=%g,select='gte(n\\,%d)*not(mod(n-%d\\,%d))',scale=%d:%d:flags=area,format=gray", cropFilter, vfps, phase, phase, every, w, h),
		"-fps_mode", "passthrough", "-f", "rawvideo", "-pix_fmt", "gray", "-")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %w", err)
	}

	type job struct {
		dets  []plan.Detection
		words int
		runs  int
		warns []string
		done  chan struct{}
	}
	var (
		jobs    []*job // one per frame read by OCR
		keys    []plan.Keyframe
		times   []float64 // every look
		jobOf   []int     // the job whose result each look shows
		sem     = make(chan struct{}, o.Concurrency)
		wg      sync.WaitGroup
		last    []float64
		lastOCR = math.Inf(-1)
	)
	ocrFrame := func(j *job, gray []byte, t float64) {
		defer wg.Done()
		defer close(j.done)
		defer func() { <-sem }()
		var img []byte
		if zoom == 2 {
			img = ocr.PGM(Upscale2x(gray, w, h), 2*w, 2*h)
		} else {
			img = ocr.PGM(gray, w, h)
		}
		for _, psm := range o.PSMs {
			words, err := ocr.Image(img, o.Lang, psm)
			if err != nil {
				msg := err.Error()
				if len(msg) > 160 {
					msg = msg[:160]
				}
				j.warns = append(j.warns, fmt.Sprintf("frame at %.1fs, OCR pass psm %d failed: %s", t, psm, msg))
				continue
			}
			j.runs++
			j.words += len(words)
			j.dets = append(j.dets, plan.DetectInRows(ocr.BuildRows(words, 0, 0), float64(zoom*w), float64(zoom*h), o.Kinds)...)
		}
	}
	// The scroll is measured meanwhile, on its own decode: it needs no OCR result.
	type motionResult struct {
		m   *Motion
		err error
	}
	motionDone := make(chan motionResult, 1)
	go func() {
		m, err := MeasureMotion(video, cropFilter, srcW, srcH, vfps)
		motionDone <- motionResult{m, err}
	}()
	r := bufio.NewReaderSize(stdout, 1<<20)
	for n := 0; ; n++ {
		gray := make([]byte, w*h)
		if _, err := io.ReadFull(r, gray); err != nil {
			break
		}
		t := float64(n*every+phase) / vfps
		means := BlockMeans(gray, w, h)
		// Compared with the last frame read: a big change (a scroll, a new window) is read now;
		// any change at all, down to one block (a code appearing, a typed character), within
		// RefreshSec; no change costs nothing.
		changed := math.MaxInt
		if last != nil {
			changed = ChangedBlocks(last, means)
		}
		big := float64(changed) >= o.BigChange*float64(len(means))
		times = append(times, t)
		if big || (changed > 0 && t-lastOCR >= o.RefreshSec-1e-9) {
			j := &job{done: make(chan struct{})}
			jobs = append(jobs, j)
			keys = append(keys, plan.Keyframe{Thumb: plan.HalfSize(gray, w, h), Times: []float64{t}})
			last, lastOCR = means, t
			sem <- struct{}{}
			wg.Add(1)
			go ocrFrame(j, gray, t)
		} else if len(keys) > 0 {
			keys[len(keys)-1].Times = append(keys[len(keys)-1].Times, t)
		}
		jobOf = append(jobOf, len(jobs)-1)
	}
	wg.Wait()
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %v %s", err, strings.TrimSpace(lastChars(stderr.String(), 300)))
	}

	// The scroll on every frame; then the parts of the page that went by between two reads
	// (a jump, a fast scroll) are read too.
	mres := <-motionDone
	motion, merr := mres.m, mres.err
	var motionWarn string
	if merr != nil {
		motionWarn = "motion not measured, masks follow the reads only: " + merr.Error()
		motion = nil
	}
	if motion != nil {
		if extra := motion.Uncovered(times); len(extra) > 0 {
			frames, err := grabFrames(video, cropFilter, motion.FPS, extra, w, h)
			if err != nil {
				motionWarn = "extra reads failed: " + err.Error()
			}
			for i, gray := range frames {
				t := float64(extra[i]) / motion.FPS
				j := &job{done: make(chan struct{})}
				jobs = append(jobs, j)
				keys = append(keys, plan.Keyframe{Thumb: plan.HalfSize(gray, w, h), Times: []float64{t}})
				times = append(times, t)
				jobOf = append(jobOf, len(jobs)-1)
				sem <- struct{}{}
				wg.Add(1)
				go ocrFrame(j, gray, t)
			}
			wg.Wait()
		}
	}

	res := &Result{Video: video, Width: info.Width, Height: info.Height, DurationSec: info.DurationSec, LookFPS: o.LookFPS, FramesScanned: len(times)}
	if motionWarn != "" {
		res.Warnings = append(res.Warnings, motionWarn)
	}
	var samples []plan.Sample
	for i, t := range times {
		var dets []plan.Detection
		if jobOf[i] >= 0 {
			dets = jobs[jobOf[i]].dets
		}
		samples = append(samples, plan.Sample{TimeSec: t, Detections: dets})
	}
	for _, j := range jobs {
		res.OCRRuns += j.runs
		res.WordsRead += j.words
		res.Warnings = append(res.Warnings, j.warns...)
	}
	if len(samples) > 0 && res.OCRRuns == 0 {
		first := "unknown"
		if len(res.Warnings) > 0 {
			first = res.Warnings[0]
		}
		return nil, fmt.Errorf("OCR failed on every frame, nothing was scanned. First error: %s", first)
	}
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].TimeSec < samples[j].TimeSec })
	tw, th := w>>1, h>>1
	linked := plan.LinkTracks(samples, 0)
	plan.Propagate(linked, keys, tw, th, 0)
	// The surface each hidden box sits on, for the soft fill.
	keyOfTime := map[float64]int{}
	for i, k := range keys {
		for _, t := range k.Times {
			keyOfTime[t] = i
		}
	}
	for _, tr := range linked {
		for i := range tr.Points {
			if k, ok := keyOfTime[tr.Points[i].T]; ok {
				tr.Points[i].Bg = plan.BgAround(keys[k].Thumb, tw, th, tr.Points[i].Box)
			}
		}
	}
	if motion != nil {
		linked = plan.FollowScroll(linked, motion.Shift, motion.Scene, motion.FPS)
	}
	sort.Float64s(times)
	res.Regions = plan.FinalizeTracks(linked, times, info.DurationSec)
	if res.Regions == nil {
		res.Regions = []*plan.Track{}
	}
	return res, nil
}

// grabFrames decodes some frames (by number at fps) at the OCR size.
func grabFrames(video, cropFilter string, fps float64, frames []int, w, h int) ([][]byte, error) {
	var sel []string
	for _, f := range frames {
		sel = append(sel, fmt.Sprintf("eq(n\\,%d)", f))
	}
	vf := fmt.Sprintf("%ssetpts=PTS-STARTPTS,fps=%g,select='%s',scale=%d:%d:flags=area,format=gray", cropFilter, fps, strings.Join(sel, "+"), w, h)
	cmd := exec.Command("ffmpeg", "-v", "error", "-i", video, "-vf", vf, "-fps_mode", "passthrough", "-f", "rawvideo", "-pix_fmt", "gray", "-")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%v %s", err, strings.TrimSpace(lastChars(stderr.String(), 300)))
	}
	var res [][]byte
	for len(out) >= w*h && len(res) < len(frames) {
		res = append(res, out[:w*h])
		out = out[w*h:]
	}
	return res, nil
}

func lastChars(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
