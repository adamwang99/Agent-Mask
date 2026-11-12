package batch

import (
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/adamwang99/agent-mask/internal/media"
	"github.com/adamwang99/agent-mask/internal/redact"
	"github.com/adamwang99/agent-mask/internal/scan"
)

// ImageOut: the output name of a picture: PNG and JPEG keep their format, others become PNG.
func ImageOut(rel string) string {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".png", ".jpg", ".jpeg":
		return rel
	}
	return strings.TrimSuffix(rel, filepath.Ext(rel)) + ".png"
}

// asClip makes a one-frame lossless clip of a picture, so the video scanner reads it.
func asClip(img, dir, name string) (string, error) {
	out := filepath.Join(dir, name+".mkv")
	b, err := exec.Command("ffmpeg", "-v", "error", "-y", "-i", img, "-frames:v", "1", "-vf", "pad=ceil(iw/2)*2:ceil(ih/2)*2,format=yuv444p", "-r", "1", "-c:v", "ffv1", out).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("read picture: %v %s", err, strings.TrimSpace(string(b)))
	}
	return out, nil
}

func readPNG(path string) (*image.NRGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	img := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(img, img.Rect, src, b.Min, draw.Src)
	return img, nil
}

// redactImage hides what the scan finds in a picture: secrets under a flat shade of their
// own background, personal data under a mosaic, like the videos. Then the output is scanned
// again, so "verified" means the same as for a video.
func redactImage(in, out string, o Options) (*FileReport, error) {
	dir, err := os.MkdirTemp("", "agent-mask-img-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	// Any picture ffmpeg reads (webp, bmp, tiff...) becomes a PNG first.
	srcPNG := filepath.Join(dir, "src.png")
	if b, err := exec.Command("ffmpeg", "-v", "error", "-y", "-i", in, "-frames:v", "1", srcPNG).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("read picture: %v %s", err, strings.TrimSpace(string(b)))
	}
	img, err := readPNG(srcPNG)
	if err != nil {
		return nil, err
	}
	clip, err := asClip(srcPNG, dir, "src")
	if err != nil {
		return nil, err
	}
	so := scan.Options{Kinds: o.Kinds, Lang: o.Lang, LookFPS: 1}
	found, err := scan.Video(clip, so)
	if err != nil {
		return nil, err
	}
	a, bb := redact.BoxesAt(found.Regions, o.Style)(0)
	W, H := img.Rect.Dx(), img.Rect.Dy()
	aLook, bLook := media.GroupLooks(o.Style)
	for _, m := range a {
		fill(img, m, W, H, aLook)
	}
	for _, m := range bb {
		mosaic(img, m, W, H, bLook)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return nil, err
	}
	if err := writeImage(out, img); err != nil {
		return nil, err
	}
	outPNG := filepath.Join(dir, "out.png")
	if err := writeImage(outPNG, img); err != nil {
		return nil, err
	}
	checkClip, err := asClip(outPNG, dir, "out")
	if err != nil {
		return nil, err
	}
	so.Check = true
	check, err := scan.Video(checkClip, so)
	if err != nil {
		return nil, err
	}
	r := &FileReport{Found: len(found.Regions), Hidden: len(a) + len(bb), Kinds: kindsOf(found)}
	r.Verified, r.Reasons = redact.Verdict(check)
	r.StillReadable = redact.Findings(check)
	// A picture whose only text was sensitive reads empty once hidden: the OCR proved it works
	// on this picture by reading the original, so an empty, error-free check is a pass.
	if !r.Verified && found.WordsRead > 0 && check.WordsRead == 0 && len(check.Regions) == 0 && len(check.Warnings) == 0 {
		r.Verified, r.Reasons = true, nil
	}
	return r, nil
}

func rect(m media.MaskBox, W, H int) (int, int, int, int) {
	x0 := max(0, int(math.Floor(m.X0*float64(W)))-1)
	y0 := max(0, int(math.Floor(m.Y0*float64(H)))-1)
	x1 := min(W, int(math.Ceil(m.X1*float64(W)))+1)
	y1 := min(H, int(math.Ceil(m.Y1*float64(H)))+1)
	return x0, y0, x1, y1
}

func fill(img *image.NRGBA, m media.MaskBox, W, H int, look string) {
	x0, y0, x1, y1 := rect(m, W, H)
	v := media.SoftShade(m.Bg)
	if look == "black" {
		v = 0
	}
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			p := img.Pix[img.PixOffset(x, y):]
			p[0], p[1], p[2], p[3] = v, v, v, 255
		}
	}
}

// mosaic: blocks of the average color (blur: a heavier block, then softened by the block size).
func mosaic(img *image.NRGBA, m media.MaskBox, W, H int, look string) {
	x0, y0, x1, y1 := rect(m, W, H)
	// Blocks sized by the text line, not the picture: half a line high leaves no letter shape.
	block := max(8, H/90, (y1-y0)/2)
	if look == "blur" {
		block = max(12, H/60, (y1-y0)*2/3)
	}
	for by := y0; by < y1; by += block {
		for bx := x0; bx < x1; bx += block {
			ex, ey := min(x1, bx+block), min(y1, by+block)
			var s [3]int
			n := 0
			for y := by; y < ey; y++ {
				for x := bx; x < ex; x++ {
					p := img.Pix[img.PixOffset(x, y):]
					s[0] += int(p[0])
					s[1] += int(p[1])
					s[2] += int(p[2])
					n++
				}
			}
			for y := by; y < ey; y++ {
				for x := bx; x < ex; x++ {
					p := img.Pix[img.PixOffset(x, y):]
					p[0], p[1], p[2] = byte(s[0]/n), byte(s[1]/n), byte(s[2]/n)
				}
			}
		}
	}
}

func writeImage(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		err = jpeg.Encode(f, img, &jpeg.Options{Quality: 95})
	default:
		err = (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(f, img)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
