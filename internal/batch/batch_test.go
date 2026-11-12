package batch

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/adamwang99/agent-mask/internal/ocr"
	"github.com/adamwang99/agent-mask/internal/scan"
)

func need(t *testing.T) {
	for _, c := range []string{"ffmpeg", "ffprobe", "tesseract"} {
		if _, err := exec.LookPath(c); err != nil {
			t.Skip("needs " + c)
		}
	}
}

func picture(t *testing.T, path, text string) {
	t.Helper()
	vf := "drawtext=text='" + text + "':fontsize=34:x=40:y=60:fontcolor=black,drawtext=text='Settings page':fontsize=34:x=40:y=180:fontcolor=black"
	if b, err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "color=c=white:s=900x300", "-vf", vf, "-frames:v", "1", path).CombinedOutput(); err != nil {
		t.Fatal(err, string(b))
	}
}

func TestFolderJob(t *testing.T) {
	need(t)
	in, out := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(in, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	picture(t, filepath.Join(in, "a.png"), "Contact jane.doe@example.com")
	picture(t, filepath.Join(in, "sub", "b.jpg"), "Call 0912 345 678")
	os.WriteFile(filepath.Join(in, "notes.txt"), []byte("not a picture"), 0o644)

	m := NewManager()
	j, err := m.Start(in, out, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if j.Counts.Total != 2 {
		t.Fatalf("%d files, want 2", j.Counts.Total)
	}
	j, err = m.Wait(j.ID, 2*time.Minute)
	if err != nil || j.Status != "done" {
		t.Fatalf("%v %+v", err, j)
	}
	for _, f := range j.Files {
		if f.Status != "verified" || f.Hidden == 0 {
			t.Fatalf("%+v", f)
		}
	}
	for _, f := range []string{"a.png", "sub/b.jpg", "report.json", "report.md"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Fatal(err)
		}
	}
	// The report never holds the hidden text.
	for _, f := range []string{"report.json", "report.md"} {
		b, _ := os.ReadFile(filepath.Join(out, f))
		for _, secret := range []string{"jane.doe", "345 678"} {
			if strings.Contains(string(b), secret) {
				t.Fatalf("%s holds %q", f, secret)
			}
		}
	}
	// A second run skips what is unchanged.
	j2, _ := m.Start(in, out, Options{})
	j2, _ = m.Wait(j2.ID, time.Minute)
	if j2.Counts.Skipped != 2 {
		t.Fatalf("second run: %+v", j2.Counts)
	}
}

func TestCheckRefusesNestedFolders(t *testing.T) {
	in := t.TempDir()
	if _, _, err := Check(in, filepath.Join(in, "out")); err == nil {
		t.Fatal("output inside input accepted")
	}
	if _, _, err := Check(in, in); err == nil {
		t.Fatal("same folder accepted")
	}
	if _, _, err := Check(filepath.Join(in, "nope"), t.TempDir()); err == nil {
		t.Fatal("missing input accepted")
	}
}

func TestCancelQueuedJob(t *testing.T) {
	need(t)
	in := t.TempDir()
	picture(t, filepath.Join(in, "a.png"), "Contact jane.doe@example.com")
	m := NewManager()
	first, err := m.Start(in, t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Start(in, t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Cancel(second.ID); err != nil {
		t.Fatal(err)
	}
	if j, _ := m.Get(second.ID); j.Status != "canceled" {
		t.Fatalf("queued job after cancel: %s", j.Status)
	}
	if j, _ := m.Wait(first.ID, time.Minute); j.Status != "done" {
		t.Fatalf("first job: %s", j.Status)
	}
	if j, _ := m.Wait(second.ID, time.Second); j.Status != "canceled" || j.Counts.Done != 0 {
		t.Fatalf("second job: %+v", j)
	}
}

func TestOutputNamesDoNotCollide(t *testing.T) {
	taken := map[string]bool{}
	a := outName("clip.mov", "video", taken)
	b := outName("clip.mp4", "video", taken)
	c := outName("sub/clip.mp4", "video", taken)
	d := outName("pic.bmp", "image", taken)
	e := outName("pic.png", "image", taken)
	if a != "clip.mp4" || b != "clip.mp4.mp4" || c != "sub/clip.mp4" || d != "pic.png" || e != "pic.png.png" {
		t.Fatal(a, b, c, d, e)
	}
}

// A file that was not verified stays "review" on the next run: a rerun must not look clean.
func TestRerunKeepsReview(t *testing.T) {
	in, out := t.TempDir(), t.TempDir()
	f := filepath.Join(in, "a.png")
	os.WriteFile(f, []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(out, ReviewDir), 0o755)
	os.WriteFile(filepath.Join(out, ReviewDir, "a.png"), []byte("x"), 0o644)
	idx := loadIndex(out)
	o := Options{Lang: "eng", Style: "auto"}
	idx.Done["a.png"] = indexEntry{Key: idx.key(f, o), Output: filepath.Join(ReviewDir, "a.png"), Verified: false}
	idx.save(out)
	m := NewManager()
	j, err := m.Start(in, out, o)
	if err != nil {
		t.Fatal(err)
	}
	j, _ = m.Wait(j.ID, time.Minute)
	if j.Counts.Review != 1 || j.Counts.Skipped != 0 || j.Files[0].Status != "review" {
		t.Fatalf("%+v %+v", j.Counts, j.Files[0])
	}
}

// One file instead of a folder: only that file is done, and the output may sit next to it.
func TestSingleFileJob(t *testing.T) {
	need(t)
	in := t.TempDir()
	picture(t, filepath.Join(in, "a.png"), "Contact jane.doe@example.com")
	picture(t, filepath.Join(in, "b.png"), "Call 0912 345 678")
	m := NewManager()
	if _, err := m.Start(filepath.Join(in, "a.png"), in, Options{}); err == nil {
		t.Fatal("the file's own folder accepted as output")
	}
	out := filepath.Join(in, "out")
	j, err := m.Start(filepath.Join(in, "a.png"), out, Options{})
	if err != nil {
		t.Fatal(err)
	}
	j, _ = m.Wait(j.ID, 2*time.Minute)
	if j.Counts.Total != 1 || j.Counts.Verified != 1 {
		t.Fatalf("%+v", j.Counts)
	}
	if _, err := os.Stat(filepath.Join(out, "a.png")); err != nil {
		t.Fatal(err)
	}
}

// Issue #1: a page of look-alike lines scrolled at 250 px/s, then jumped back. The masks must
// be on the secrets on every frame: each frame of the output is read, none shows them. The
// report says when each secret was on screen, not "the whole video".
func TestFastScrollAndJumpVideo(t *testing.T) {
	need(t)
	if testing.Short() {
		t.Skip("reads every frame of a video")
	}
	in, out := t.TempDir(), t.TempDir()
	page, video := filepath.Join(in, "page.png"), filepath.Join(in, "scroll.mp4")
	run := func(args ...string) {
		t.Helper()
		if b, err := exec.Command("ffmpeg", append([]string{"-v", "error", "-y"}, args...)...).CombinedOutput(); err != nil {
			t.Fatal(err, string(b))
		}
	}
	// A 1280 x 2400 page, a line every 46 px; two of them hold a secret.
	vf := ""
	for i := 0; 40+46*i < 2370; i++ {
		text := fmt.Sprintf("Step %02d open the settings page and check item %d", i, i*7%13)
		switch i {
		case 8:
			text = "Owner jane.doe@example.com"
		case 22:
			text = "API_KEY=sk-proj-AbC123xYz789QwErTy456UiOp"
		}
		if i > 0 {
			vf += ","
		}
		vf += fmt.Sprintf("drawtext=text='%s':fontsize=24:x=60:y=%d:fontcolor=black", text, 40+46*i)
	}
	run("-f", "lavfi", "-i", "color=c=white:s=1280x2400", "-vf", vf, "-frames:v", "1", page)
	// 0-1 s still, 1-4 s 250 px/s, 4-4.5 s back 600 px, 4.5-5.5 s still.
	y := "if(lt(t,1),0,if(lt(t,4),250*(t-1),if(lt(t,4.5),750-1200*(t-4),150)))"
	run("-loop", "1", "-framerate", "30", "-i", page, "-vf", "crop=1280:720:0:'"+y+"',format=yuv420p", "-t", "5.5", "-c:v", "libx264", "-crf", "18", "-r", "30", video)
	if err := os.Remove(page); err != nil {
		t.Fatal(err)
	}

	m := NewManager()
	j, err := m.Start(video, out, Options{})
	if err != nil {
		t.Fatal(err)
	}
	j, _ = m.Wait(j.ID, 5*time.Minute)
	f := j.Files[0]
	if f.Status != "verified" || len(f.StillReadable) != 0 {
		t.Fatalf("%s: %v %+v", f.Status, f.Reasons, f.StillReadable)
	}
	// The email is on screen at the start, scrolls away, and is back after the jump; neither
	// secret is reported on screen for the whole video.
	twice := false
	for _, r := range f.Regions {
		if r.StartSec == 0 && r.EndSec > 5.4 && len(r.OnScreen) == 1 {
			t.Errorf("%s reported on screen for the whole video: %+v", r.Kind, r)
		}
		if r.Kind == "email" && len(r.OnScreen) == 2 {
			twice = true
		}
	}
	if !twice {
		t.Errorf("the email was on screen twice: %+v", f.Regions)
	}

	// Every frame of the output, read at twice its size.
	const w, h = 1280, 720
	raw, err := exec.Command("ffmpeg", "-v", "error", "-i", f.Output, "-f", "rawvideo", "-pix_fmt", "gray", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	frames := len(raw) / (w * h)
	if frames < 160 {
		t.Fatalf("%d frames in the output", frames)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var leaks []string
	words := 0
	sem := make(chan struct{}, max(1, runtime.NumCPU()/2))
	for n := 0; n < frames; n++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(n int) {
			defer wg.Done()
			defer func() { <-sem }()
			img := ocr.PGM(scan.Upscale2x(raw[n*w*h:(n+1)*w*h], w, h), 2*w, 2*h)
			for _, psm := range []int{6} {
				ws, err := ocr.Image(img, "eng", psm)
				if err != nil {
					t.Error(err)
					return
				}
				text := ""
				for _, wd := range ws {
					text += wd.Text + " "
				}
				mu.Lock()
				words += len(ws)
				for _, secret := range []string{"jane.doe", "example.com", "sk-proj", "AbC123"} {
					if strings.Contains(text, secret) {
						leaks = append(leaks, fmt.Sprintf("frame %d (%.2fs): %s", n, float64(n)/30, secret))
					}
				}
				mu.Unlock()
			}
		}(n)
	}
	wg.Wait()
	if words < 20*frames {
		t.Fatalf("the check read %d words in %d frames: it cannot vouch for them", words, frames)
	}
	if len(leaks) > 0 {
		sort.Strings(leaks)
		t.Fatalf("%d secret(s) readable in the output, first: %s", len(leaks), leaks[0])
	}
}
