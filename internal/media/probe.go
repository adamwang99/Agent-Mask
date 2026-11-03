// Package media: video facts from ffprobe, and rendering through ffmpeg.
package media

import (
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
)

// Info about a video's first video stream.
type Info struct {
	Width, Height int
	DurationSec   float64
	FPS           float64
}

// Probe reads size, length and frame rate.
func Probe(video string) (Info, error) {
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height,r_frame_rate:format=duration", "-of", "json", video).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return Info{}, fmt.Errorf("ffprobe %s: %s", video, strings.TrimSpace(string(ee.Stderr)))
		}
		return Info{}, fmt.Errorf("ffprobe: %w", err)
	}
	var j struct {
		Streams []struct {
			Width, Height int
			RFrameRate    string `json:"r_frame_rate"`
		}
		Format struct{ Duration string }
	}
	if err := json.Unmarshal(out, &j); err != nil || len(j.Streams) == 0 {
		return Info{}, fmt.Errorf("%s has no video stream", video)
	}
	st := j.Streams[0]
	fps := 30.0
	if num, den, ok := strings.Cut(st.RFrameRate, "/"); ok {
		n, _ := strconv.ParseFloat(num, 64)
		d, _ := strconv.ParseFloat(den, 64)
		if d > 0 && n > 0 {
			fps = math.Round(n/d*100) / 100
		}
	}
	dur, _ := strconv.ParseFloat(j.Format.Duration, 64)
	return Info{Width: st.Width, Height: st.Height, DurationSec: dur, FPS: fps}, nil
}
