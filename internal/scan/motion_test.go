package scan

import (
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
