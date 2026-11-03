package scan

import (
	"math/rand"
	"testing"
)

// Change detection ignores noise, sees a small code appearing.
func TestChangeDetection(t *testing.T) {
	w, h := 320, 160
	a := make([]byte, w*h)
	for i := range a {
		a[i] = 230
	}
	noisy := append([]byte(nil), a...)
	for i := range noisy {
		noisy[i] = byte(230 + (rand.Intn(80)-40)%26)
	}
	if ChangedBlocks(BlockMeans(a, w, h), BlockMeans(noisy, w, h)) != 0 {
		t.Fatal("noise is not a change")
	}
	code := append([]byte(nil), a...)
	for y := 20; y < 30; y++ {
		for x := 40; x < 50; x++ {
			code[y*w+x] = 20
		}
	}
	if ChangedBlocks(BlockMeans(a, w, h), BlockMeans(code, w, h)) < 1 {
		t.Fatal("one block of new text is a change")
	}
	if up := Upscale2x([]byte{0, 100, 200, 100}, 2, 2); len(up) != 16 || up[1] != 50 {
		t.Fatal("upscale")
	}
	if w, h := OCRSize(3840, 2160); w != 2560 || h != 1440 {
		t.Fatal("4K is read at 2560")
	}
}
