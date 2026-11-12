package batch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
