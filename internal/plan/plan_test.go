package plan

import (
	"math"
	"testing"
)

func box(y float64) Box { return Box{X0: 0.1, Y0: y, X1: 0.5, Y1: y + 0.03} }
func det(kind, text string, y float64) Detection {
	return Detection{Kind: kind, Text: text, Box: box(y)}
}

const key = "sk-proj-AbC123xYz789QwErTy456UiOp"

// A track follows text that scrolls, and the mask is where the text is.
func TestTrackFollowsScroll(t *testing.T) {
	s := []Sample{
		{0, []Detection{det("api-key", key, 0.6)}},
		{1, []Detection{det("api-key", key, 0.6)}},
		{1.2, []Detection{det("api-key", "sk-proj-AbC123xYz789QwErTy456Ui0p", 0.5)}},
		{1.4, []Detection{det("api-key", key, 0.4)}},
		{1.6, []Detection{det("api-key", "sk-proj-AbCl23xYz789QwErTy456UiOp", 0.3)}},
		{2.6, []Detection{det("api-key", key, 0.3)}},
	}
	tracks := BuildTracks(s, 4, 0)
	if len(tracks) != 1 {
		t.Fatalf("%d tracks: one per position instead of one", len(tracks))
	}
	tr := tracks[0]
	at := func(T float64) Box {
		b, _, ok := tr.BoxAt(T)
		if !ok {
			t.Fatalf("not covered at %v", T)
		}
		return b
	}
	if b := at(0.5); b.Y0 > 0.6 || b.Y1 < 0.63 {
		t.Fatal("start position")
	}
	if b := at(1.3); b.Y0 > 0.4 || b.Y1 < 0.53 {
		t.Fatal("must cover everywhere the text passed")
	}
	if b := at(2.2); b.Y0 < 0.29 || b.Y1 > 0.34 {
		t.Fatal("no longer at the old spot")
	}
	at(3.9) // no later look proves it left: covered to the end
}

func TestMissedFramesStayCovered(t *testing.T) {
	var s []Sample
	for _, T := range []float64{0, 1, 2, 3, 4, 5} {
		var d []Detection
		if T != 2 && T != 3 {
			d = []Detection{det("api-key", key, 0.4)}
		}
		s = append(s, Sample{T, d})
	}
	tr := BuildTracks(s, 6, 0)[0]
	for _, T := range []float64{2, 2.5, 3} {
		if _, _, ok := tr.BoxAt(T); !ok {
			t.Fatalf("uncovered at %v", T)
		}
	}
}

func TestNeighbouringLinesStaySeparate(t *testing.T) {
	var s []Sample
	for _, T := range []float64{0, 1} {
		s = append(s, Sample{T, []Detection{det("email", "jane.doe@example.com", 0.3), det("api-key", key, 0.34)}})
	}
	if n := len(BuildTracks(s, 2, 0)); n != 2 {
		t.Fatalf("%d tracks", n)
	}
}

func TestTrackEndsWhenNoLongerSeen(t *testing.T) {
	s := []Sample{{0, []Detection{det("email", "jane.doe@example.com", 0.3)}}, {1, []Detection{det("email", "jane.doe@example.com", 0.3)}}, {7, nil}, {8, nil}}
	tr := BuildTracks(s, 9, 0)[0]
	if _, _, ok := tr.BoxAt(6.9); !ok {
		t.Fatal("6.9")
	}
	if _, _, ok := tr.BoxAt(7.5); ok {
		t.Fatal("still covered after it left")
	}
}

// Soft fill shade: the surface inside the box, not what lies outside the window.
func TestBgInsideTheBox(t *testing.T) {
	tw, th := 40, 20
	img := make([]byte, tw*th)
	for y := 5; y < 20; y++ {
		for x := 0; x < tw; x++ {
			img[y*tw+x] = 250
		}
	}
	for x := 4; x < 30; x += 3 {
		img[7*tw+x] = 30
	}
	if bg := BgAround(img, tw, th, Box{X0: 0.1, Y0: 5.0 / 20, X1: 0.8, Y1: 9.0 / 20}); bg == nil || *bg < 230 {
		t.Fatal("background")
	}
}

func TestFollowScrollCoversBetweenReadsAndEdges(t *testing.T) {
	// The page scrolls 0.02 of the height per frame for 60 frames (30 fps).
	shift := make([]float64, 60)
	scene := make([]int, 60)
	for f := range shift {
		shift[f] = 0.02 * float64(f)
	}
	// Read twice: at frame 15 (y 0.5) and frame 30 (y 0.2). Page y = 0.8.
	tr := &Track{Kind: "api-key", Points: []Point{
		{T: 0.5, Box: Box{X0: 0.1, Y0: 0.5, X1: 0.6, Y1: 0.53}},
		{T: 1.0, Box: Box{X0: 0.1, Y0: 0.2, X1: 0.6, Y1: 0.23}},
	}}
	tracks := FinalizeTracks(FollowScroll([]*Track{tr}, shift, scene, 30), []float64{0.5, 1.0}, 2)
	tr = tracks[0]
	for f := 0; f < 60; f++ {
		T := float64(f) / 30
		y := 0.8 - shift[f]
		box, _, ok := tr.BoxAt(T)
		visible := y+0.03 > 0 && y < 1
		if visible && (!ok || box.Y0 > math.Max(0, y)+1e-6 || box.Y1 < math.Min(1, y+0.03)-1e-6) {
			t.Fatalf("frame %d: text at %.3f, mask %+v ok=%v", f, y, box, ok)
		}
	}
	// Scrolled out of view: nothing drawn.
	if _, _, ok := tr.BoxAt(59.0 / 30); ok {
		t.Fatal("masked after it left the screen")
	}
	// It is on screen from the start until it leaves at the top (page y 0.8, 0.03 high, gone
	// when the page has scrolled 0.83 and a little margin: frame 42), not for the whole video.
	if len(tr.OnScreen) != 1 || tr.StartSec != 0 || tr.EndSec < 41.0/30 || tr.EndSec > 43.0/30 {
		t.Fatalf("on screen %+v (%v to %v), want one stretch from 0 to about 1.4", tr.OnScreen, tr.StartSec, tr.EndSec)
	}
}

// A page that scrolls away and comes back (a jump back): the secret is reported on screen
// twice, not from the first frame to the last (issue #1, B).
func TestOnScreenStretches(t *testing.T) {
	shift := make([]float64, 150)
	scene := make([]int, 150)
	for f := range shift {
		switch {
		case f < 30: // still
		case f < 60: // scrolls two screens down in a second
			shift[f] = 2 * float64(f-30) / 30
		case f < 90: // stays there
			shift[f] = 2
		case f < 96: // jumps back in 0.2 s
			shift[f] = 2 - 2*float64(f-90)/6
		}
	}
	tr := &Track{Kind: "email", Points: []Point{{T: 0.5, Box: Box{X0: 0.1, Y0: 0.5, X1: 0.5, Y1: 0.53}}}}
	tr = FinalizeTracks(FollowScroll([]*Track{tr}, shift, scene, 30), []float64{0.5}, 5)[0]
	if len(tr.OnScreen) != 2 {
		t.Fatalf("%d stretches on screen, want 2: %+v", len(tr.OnScreen), tr.OnScreen)
	}
	a, b := tr.OnScreen[0], tr.OnScreen[1]
	// Leaves at the top once the page has scrolled about 0.53: frame 38. Back during the jump:
	// frame 95. Then until the end.
	if a.StartSec != 0 || math.Abs(a.EndSec-38.0/30) > 0.07 || math.Abs(b.StartSec-95.0/30) > 0.07 || b.EndSec != 5 {
		t.Fatalf("stretches %+v", tr.OnScreen)
	}
	if tr.StartSec != a.StartSec || tr.EndSec != b.EndSec {
		t.Fatalf("track %v to %v", tr.StartSec, tr.EndSec)
	}
	for f, want := range map[int]bool{10: true, 45: false, 80: false, 120: true} {
		if _, _, ok := tr.BoxAt(float64(f) / 30); ok != want {
			t.Fatalf("frame %d: masked %v, want %v", f, ok, want)
		}
	}
}

// The same text at two places on the page: each place is followed on its own.
func TestFollowScrollTwoPlaces(t *testing.T) {
	shift := make([]float64, 60)
	scene := make([]int, 60)
	for f := range shift {
		shift[f] = 0.01 * float64(f)
	}
	// Page places 0.5 and 0.9; read three times at 0.5, once at 0.9 (frame 30: screen 0.6).
	tr := &Track{Kind: "email", Points: []Point{
		{T: 0, Box: Box{X0: 0.1, Y0: 0.5, X1: 0.5, Y1: 0.53}},
		{T: 0.5, Box: Box{X0: 0.1, Y0: 0.35, X1: 0.5, Y1: 0.38}},
		{T: 1.0, Box: Box{X0: 0.1, Y0: 0.2, X1: 0.5, Y1: 0.23}},
		{T: 1.0, Box: Box{X0: 0.1, Y0: 0.6, X1: 0.5, Y1: 0.63}},
	}}
	tracks := FinalizeTracks(FollowScroll([]*Track{tr}, shift, scene, 30), []float64{0, 0.5, 1}, 2)
	if len(tracks) != 2 {
		t.Fatalf("%d tracks, want 2", len(tracks))
	}
	for f := 0; f < 60; f++ {
		T := float64(f) / 30
		for _, page := range []float64{0.5, 0.9} {
			y := page - shift[f]
			if y < 0 || y+0.03 > 1 {
				continue
			}
			covered := false
			for _, tr := range tracks {
				if b, _, ok := tr.BoxAt(T); ok && b.Y0 <= y+1e-6 && b.Y1 >= y+0.03-1e-6 {
					covered = true
				}
			}
			if !covered {
				t.Fatalf("frame %d: text at %.2f not covered", f, y)
			}
		}
	}
}

// Sightings list only the frames OCR read, not the points worked out from them.
func TestSightingsAreTheReads(t *testing.T) {
	shift := make([]float64, 60)
	scene := make([]int, 60)
	for f := range shift {
		shift[f] = 0.01 * float64(f)
	}
	tr := &Track{Kind: "email", Points: []Point{{T: 1.0, Box: Box{X0: 0.1, Y0: 0.6, X1: 0.5, Y1: 0.63}}}}
	tracks := FinalizeTracks(FollowScroll([]*Track{tr}, shift, scene, 30), []float64{1}, 2)
	if len(tracks) != 1 || len(tracks[0].Points) < 2 {
		t.Fatalf("want one followed track, got %+v", tracks)
	}
	s := tracks[0].Sightings
	if len(s) != 1 || s[0].T != 1.0 || s[0].Box.Y0 != 0.6 {
		t.Fatalf("sightings %+v, want the one read at 1.0 s, y 0.6", s)
	}
}
