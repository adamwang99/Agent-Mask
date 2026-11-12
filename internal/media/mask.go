package media

import (
	"bufio"
	"bytes"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/adamwang99/agent-mask/internal/plan"
)

// Two mask groups, each with its own look. Secrets go to group A, contact details to group B.
var secretKinds = map[string]bool{"api-key": true, "jwt": true, "private-key": true, "secret-assignment": true, "url-credentials": true, "high-entropy": true, "credit-card": true, "mask": true}

// Styles a user can choose.
var Styles = []string{"auto", "soft", "noise", "static", "mosaic", "blur", "solid"}

// StyleFor: which group (A or B) a kind is drawn in for a style.
func StyleFor(kind, style string) string {
	switch style {
	case "soft", "noise", "static", "solid":
		return "A"
	case "mosaic", "blur":
		return "B"
	}
	if secretKinds[kind] {
		return "A"
	}
	return "B"
}

// GroupLooks: how each group looks for a style.
func GroupLooks(style string) (a, b string) {
	a = map[string]string{"solid": "black", "noise": "noise", "static": "static"}[style]
	if a == "" {
		a = "soft"
	}
	b = "mosaic"
	if style == "blur" {
		b = "blur"
	}
	return
}

// SoftShade: fill shade of a hidden box: its background, nudged 26 levels toward mid-gray.
func SoftShade(bg *float64) byte {
	v := 230.0
	if bg != nil {
		v = *bg
	}
	if v > 127 {
		return byte(math.Max(0, v-26))
	}
	return byte(math.Min(255, v+26))
}

// MaskBox is a box to hide, normalized, with the surface's gray under it (nil: unknown).
type MaskBox struct {
	plan.Box
	Bg *float64
}

// DrawMaskFrame fills one mask frame: three side-by-side gray planes at 1/scale of the video:
// [ group A mask | group B mask | group A fill shade ]. Masks are 255 where to hide; boxes are
// rounded outwards, so the scale never uncovers a pixel.
func DrawMaskFrame(buf []byte, mw, mh int, a, b []MaskBox) {
	clear(buf)
	stride := 3 * mw
	paint := func(bx plan.Box, offset int, v byte) {
		x0, x1 := max(0, int(math.Floor(bx.X0*float64(mw)))), min(mw, int(math.Ceil(bx.X1*float64(mw))))
		y0, y1 := max(0, int(math.Floor(bx.Y0*float64(mh)))), min(mh, int(math.Ceil(bx.Y1*float64(mh))))
		for y := y0; y < y1; y++ {
			row := buf[y*stride+offset+x0 : y*stride+offset+max(x0, x1)]
			for i := range row {
				row[i] = v
			}
		}
	}
	for _, m := range a {
		paint(m.Box, 0, 255)
		paint(m.Box, 2*mw, SoftShade(m.Bg))
	}
	for _, m := range b {
		paint(m.Box, mw, 255)
	}
}

func fillASource(look string, W, H int, fps float64) string {
	q := fmt.Sprintf("%dx%d", (W+3)/4, (H+3)/4)
	switch look {
	case "noise":
		return fmt.Sprintf("color=c=0x8c8c8c:s=%s:r=%s", q, num(fps/2))
	case "static":
		return fmt.Sprintf("color=c=0x8c8c8c:s=%s:r=%s", q, num(fps))
	case "black":
		return fmt.Sprintf("color=c=black:s=%dx%d:r=%s", W, H, num(fps))
	}
	return "" // soft: the shade plane of the mask stream
}

// fillAFilter makes [fa], group A's fill. None of them reads the footage: whatever is averaged
// or enhanced, a hidden box holds nothing of the text under it.
func fillAFilter(look string, W, H int, fps float64, mw, mh int) string {
	switch look {
	case "noise":
		return fmt.Sprintf("[2:v]format=yuv420p,noise=c0s=48:c0f=t+u,fps=%s,scale=%d:%d:flags=neighbor[fa];[m3]nullsink", num(fps), W, H)
	case "static":
		return fmt.Sprintf("[2:v]format=yuv420p,noise=c0s=48:c0f=u,trim=end_frame=1,loop=loop=-1:size=1,setpts=N/%s/TB,scale=%d:%d:flags=neighbor[fa];[m3]nullsink", num(fps), W, H)
	case "black":
		return "[2:v]format=yuv420p[fa];[m3]nullsink"
	}
	return fmt.Sprintf("[m3]crop=%d:%d:%d:0,scale=%d:%d:flags=neighbor,format=yuv420p[fa]", mw, mh, 2*mw, W, H)
}

func num(v float64) string { return strconv.FormatFloat(math.Round(v*10000)/10000, 'f', -1, 64) }

var (
	graphOptOnce sync.Once
	graphOpt     string
)

// graphFileOption: the option that reads a filter graph from a file changed (ffmpeg 6:
// -filter_complex_script, 8+: -/filter_complex, 7: both); asked of the installed ffmpeg.
func graphFileOption() string {
	graphOptOnce.Do(func() {
		dir, _ := os.MkdirTemp("", "ar-probe-")
		defer os.RemoveAll(dir)
		f := filepath.Join(dir, "g.txt")
		_ = os.WriteFile(f, []byte("null"), 0o644)
		graphOpt = "-filter_complex_script"
		if exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "nullsrc=s=16x16:d=0.04", "-/filter_complex", f, "-f", "null", "-").Run() == nil {
			graphOpt = "-/filter_complex"
		}
	})
	return graphOpt
}

// Request is one masking render.
type Request struct {
	Input, Output string
	Info          Info
	BoxesAt       func(T float64) (a, b []MaskBox)
	Style         string
}

// Render hides the boxes in one ffmpeg pass: one mask frame per video frame is piped in, so a
// mask follows text that moves; the sound is copied as it is.
func Render(r Request) error {
	W, H := r.Info.Width, r.Info.Height
	fps := r.Info.FPS
	if fps == 0 {
		fps = 30
	}
	aLook, bLook := GroupLooks(r.Style)
	const maskScale = 4
	mw, mh := (W+maskScale-1)/maskScale, (H+maskScale-1)/maskScale
	block := max(8, int(math.Round(float64(H)/90)))
	parts := []string{
		fmt.Sprintf("[0:v]setpts=PTS-STARTPTS,fps=%s,format=yuv420p,split=2[base][src]", num(fps)),
		"[1:v]setpts=PTS-STARTPTS,split=3[m1][m2][m3]",
		fmt.Sprintf("[m1]crop=%d:%d:0:0,scale=%d:%d:flags=neighbor,format=gray[ma]", mw, mh, W, H),
		fmt.Sprintf("[m2]crop=%d:%d:%d:0,scale=%d:%d:flags=neighbor,format=gray[mb]", mw, mh, mw, W, H),
		fillAFilter(aLook, W, H, fps, mw, mh),
		"[fa][ma]alphamerge[ea]",
	}
	if bLook == "blur" {
		parts = append(parts, fmt.Sprintf("[src]boxblur=luma_radius=%d:luma_power=3:chroma_radius=%d[fb]", block, (block+1)/2))
	} else {
		parts = append(parts, fmt.Sprintf("[src]scale=%d:%d:flags=area,scale=%d:%d:flags=neighbor[fb]", (W+block-1)/block, (H+block-1)/block, W, H))
	}
	parts = append(parts, "[fb][mb]alphamerge[eb]", "[base][eb]overlay=shortest=1[o1]", "[o1][ea]overlay=shortest=1[out]")
	graph := strings.Join(parts, ";")
	args := []string{"-v", "error", "-y", "-i", r.Input,
		"-f", "rawvideo", "-pix_fmt", "gray", "-s", fmt.Sprintf("%dx%d", 3*mw, mh), "-framerate", num(fps), "-i", "pipe:0"}
	if src := fillASource(aLook, W, H, fps); src != "" {
		args = append(args, "-f", "lavfi", "-i", src)
	}
	graphArgs := []string{"-filter_complex", graph}
	if len(graph) >= 6000 {
		dir, err := os.MkdirTemp("", "agent-mask-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		gf := filepath.Join(dir, "graph.txt")
		if err := os.WriteFile(gf, []byte(graph), 0o644); err != nil {
			return err
		}
		graphArgs = []string{graphFileOption(), gf}
	}
	args = append(args, graphArgs...)
	// No metadata is carried over: a title, a location or a device name can be sensitive too.
	args = append(args, "-map", "[out]", "-map", "0:a?", "-map_metadata", "-1", "-map_chapters", "-1", "-c:v", "libx264", "-preset", "veryfast", "-crf", "20", "-pix_fmt", "yuv420p",
		"-tune", "stillimage", "-c:a", "aac", "-b:a", "160k", "-movflags", "+faststart", r.Output)
	cmd := exec.Command("ffmpeg", args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ffmpeg: %w", err)
	}
	w := bufio.NewWriterSize(stdin, 1<<20)
	frames := int(math.Ceil(r.Info.DurationSec*fps)) + int(math.Ceil(fps))
	buf := make([]byte, 3*mw*mh)
	for n := 0; n < frames; n++ {
		a, b := r.BoxesAt(float64(n) / fps)
		DrawMaskFrame(buf, mw, mh, a, b)
		if _, err := w.Write(buf); err != nil {
			break // ffmpeg ended early: its exit status says why
		}
	}
	_ = w.Flush()
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		e := strings.TrimSpace(stderr.String())
		if len(e) > 400 {
			e = e[len(e)-400:]
		}
		return fmt.Errorf("ffmpeg: %v: %s", err, e)
	}
	return nil
}
