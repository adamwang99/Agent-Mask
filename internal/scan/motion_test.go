package scan

import (
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"testing"
)

// A tall page scrolled slowly, then fast, then jumped back, then a different picture: the
// measured scroll follows the true one within a few pixels, and the new picture is a new scene.
func TestMeasureMotion(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	page, video := filepath.Join(dir, "page.png"), filepath.Join(dir, "v.mp4")
	run := func(args ...string) {
		if b, err := exec.Command("ffmpeg", append([]string{"-v", "error", "-y"}, args...)...).CombinedOutput(); err != nil {
			t.Fatal(err, string(b))
		}
	}
	// A page of random-looking blocks (texture like text), 640 x 2400.
	run("-f", "lavfi", "-i", "cellauto=s=640x2400:rule=30:random_fill_ratio=0.4", "-frames:v", "1", page)
	// 0-1 s still, 1-3 s 20 px/s, 3-4 s 400 px/s, 4-4.3 s jump back 300 px, 4.3-5 s still,
	// 5-6 s another picture.
	y := "if(lt(t,1),0,if(lt(t,3),20*(t-1),if(lt(t,4),40+400*(t-3),if(lt(t,4.3),440-1000*(t-4),140))))"
	run("-loop", "1", "-framerate", "30", "-i", page, "-f", "lavfi", "-i", "testsrc2=s=640x360:r=30:d=1",
		"-filter_complex", "[0:v]crop=640:360:0:'"+y+"',trim=duration=5[a];[a][1:v]concat=n=2:v=1,format=yuv420p[v]",
		"-map", "[v]", "-c:v", "libx264", "-crf", "16", "-r", "30", video)
	m, err := MeasureMotion(video, "", 640, 360, 30)
	if err != nil {
		t.Fatal(err)
	}
	truth := func(t float64) float64 {
		switch {
		case t < 1:
			return 0
		case t < 3:
			return 20 * (t - 1)
		case t < 4:
			return 40 + 400*(t-3)
		case t < 4.3:
			return 440 - 1000*(t-4)
		}
		return 140
	}
	for _, at := range []float64{0.5, 2, 2.9, 3.5, 3.97, 4.15, 4.6} {
		f := m.FrameAt(at)
		got := m.Shift[f] * 360
		if d := math.Abs(got - truth(float64(f)/30)); d > 4 {
			t.Errorf("at %.2fs: scroll %.1f px, want %.1f", at, got, truth(float64(f)/30))
		}
	}
	if m.Scene[m.FrameAt(4.8)] != m.Scene[0] {
		t.Errorf("a scroll was taken for a new scene")
	}
	if m.Scene[m.FrameAt(5.5)] == m.Scene[m.FrameAt(4.8)] {
		t.Errorf("the new picture was not a new scene")
	}
}

// textPage draws a tall white page of text lines that look alike, one every 46 px: what a
// wiki or a settings page looks like to the scroll measure (mostly blank, every line similar).
func textPage(t *testing.T, path string, w, h int) {
	t.Helper()
	vf := ""
	for i := 0; 40+46*i < h-30; i++ {
		if i > 0 {
			vf += ","
		}
		vf += fmt.Sprintf("drawtext=text='Step %02d open the settings page and check item %d':fontsize=22:x=60:y=%d:fontcolor=black", i, i*7%13, 40+46*i)
	}
	if b, err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", fmt.Sprintf("color=c=white:s=%dx%d", w, h), "-vf", vf, "-frames:v", "1", path).CombinedOutput(); err != nil {
		t.Fatal(err, string(b))
	}
}

// A page of text scrolled at 250 px/s, then jumped back 600 px in half a second (issue #1):
// on every frame the measured scroll is the true one. 0.1.3 took one line for another there
// and was a line (46 px) off for seconds.
func TestMeasureMotionFastScrollAndJump(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	page, video := filepath.Join(dir, "page.png"), filepath.Join(dir, "v.mp4")
	textPage(t, page, 1280, 3000)
	// 0-1 s still, 1-4 s 250 px/s, 4-4.5 s back 600 px, 4.5-5.5 s still.
	y := "if(lt(t,1),0,if(lt(t,4),250*(t-1),if(lt(t,4.5),750-1200*(t-4),150)))"
	if b, err := exec.Command("ffmpeg", "-v", "error", "-y", "-loop", "1", "-framerate", "30", "-i", page,
		"-vf", "crop=1280:720:0:'"+y+"',format=yuv420p", "-t", "5.5", "-c:v", "libx264", "-crf", "18", "-r", "30", video).CombinedOutput(); err != nil {
		t.Fatal(err, string(b))
	}
	m, err := MeasureMotion(video, "", 1280, 720, 30)
	if err != nil {
		t.Fatal(err)
	}
	truth := func(t float64) float64 {
		switch {
		case t < 1:
			return 0
		case t < 4:
			return 250 * (t - 1)
		case t < 4.5:
			return 750 - 1200*(t-4)
		}
		return 150
	}
	if len(m.Shift) < 160 {
		t.Fatalf("%d frames", len(m.Shift))
	}
	for f := range m.Shift {
		if m.Scene[f] != 0 {
			t.Fatalf("frame %d: a scroll was taken for a new scene", f)
		}
		if got, want := m.Shift[f]*720, truth(float64(f)/30); math.Abs(got-want) > 4 {
			t.Errorf("frame %d (%.2fs): scroll %.1f px, want %.1f", f, float64(f)/30, got, want)
		}
	}
	// The frames where the jump starts and ends are read again.
	jumps := m.Jumps(nil)
	if len(jumps) != 2 || jumps[0] < 120 || jumps[0] > 122 || jumps[1] < 135 || jumps[1] > 137 {
		t.Errorf("jump frames %v, want one near 121 and one near 136", jumps)
	}
}
