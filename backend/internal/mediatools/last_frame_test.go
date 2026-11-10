package mediatools

import (
	"context"
	"encoding/json"
	"errors"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

func TestExtractLastVideoFrameNative(t *testing.T) {
	ffmpeg, err := ResolveFFmpeg()
	if err != nil {
		t.Skip(err)
	}
	ffprobe, err := ResolveFFprobe()
	if err != nil {
		t.Skip(err)
	}
	cases := []struct {
		name, source, filter string
		extraInputs, maps    []string
		vfr                  bool
	}{
		{name: "only-last-40ms-is-blue", source: "color=c=red:s=64x48:r=25:d=0.8", filter: "drawbox=c=blue:t=fill:enable=eq(n\\,19)"},
		{name: "variable-frame-rate", source: "color=c=red:s=64x48:r=25:d=0.16", filter: "drawbox=c=blue:t=fill:enable=eq(n\\,3),setpts=N+16*gte(N\\,2)", vfr: true},
		{name: "single-frame", source: "color=c=blue:s=64x48:r=25:d=0.04", filter: "null"},
		{name: "audio-outlasts-video", source: "color=c=red:s=64x48:r=25:d=0.8", filter: "drawbox=c=blue:t=fill:enable=eq(n\\,19)", extraInputs: []string{"-f", "lavfi", "-i", "sine=frequency=440:duration=2"}, maps: []string{"-map", "0:v:0", "-map", "1:a:0", "-c:a", "aac"}},
		{name: "larger-alternate-video-stream", source: "color=c=red:s=64x48:r=25:d=0.8", filter: "drawbox=c=blue:t=fill:enable=eq(n\\,19)", extraInputs: []string{"-f", "lavfi", "-i", "color=c=green:s=128x96:r=25:d=0.8"}, maps: []string{"-map", "0:v:0", "-map", "1:v:0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			input, output := filepath.Join(dir, "视频 source.mp4"), filepath.Join(dir, "last.png")
			format := "mov"
			if tc.vfr {
				// Matroska preserves these deliberately uneven timestamps without an
				// MP4 edit list discarding the final packet at the declared end.
				input, format = filepath.Join(dir, "视频 source.mkv"), "matroska"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			args := []string{"-v", "error", "-nostdin", "-f", "lavfi", "-i", tc.source}
			args = append(args, tc.extraInputs...)
			args = append(args, tc.maps...)
			args = append(args, "-filter:v:0", tc.filter, "-fps_mode", "vfr", "-c:v", "libx264", "-bf", "2", "-pix_fmt", "yuv420p", input)
			cmd := exec.CommandContext(ctx, ffmpeg, args...)
			HideConsole(cmd)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("native fixture: %s %v", out, err)
			}
			if tc.vfr {
				assertVariableFrameIntervals(t, ctx, ffprobe, input)
			}
			if err := ExtractLastVideoFrame(ctx, input, format, output); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(output)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			picture, err := png.Decode(file)
			if err != nil {
				t.Fatal(err)
			}
			if picture.Bounds().Dx() != 64 || picture.Bounds().Dy() != 48 {
				t.Fatalf("wrong video stream: %v", picture.Bounds())
			}
			r, g, b, _ := picture.At(32, 24).RGBA()
			if b < 220*257 || r > 30*257 || g > 30*257 {
				t.Fatalf("last frame was not blue: rgb=%d,%d,%d", r/257, g/257, b/257)
			}
		})
	}
}

func assertVariableFrameIntervals(t *testing.T, ctx context.Context, ffprobe, input string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, ffprobe, "-v", "error", "-select_streams", "v:0", "-show_frames", "-show_entries", "frame=best_effort_timestamp_time", "-of", "json", input)
	HideConsole(cmd)
	raw, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Frames []struct {
			Time string `json:"best_effort_timestamp_time"`
		} `json:"frames"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil || len(probe.Frames) != 4 {
		t.Fatalf("unexpected VFR fixture: %s %v", raw, err)
	}
	times := make([]float64, len(probe.Frames))
	for i, frame := range probe.Frames {
		value, err := strconv.ParseFloat(frame.Time, 64)
		if err != nil {
			t.Fatal(err)
		}
		times[i] = value
	}
	if times[2]-times[1] < 0.5 || times[3]-times[2] > 0.1 {
		t.Fatalf("fixture is not variable-rate with a short final frame: %v", times)
	}
}

func TestExtractLastVideoFrameRejectsMissingVideo(t *testing.T) {
	ffmpeg, err := ResolveFFmpeg()
	if err != nil {
		t.Skip(err)
	}
	if _, err := ResolveFFprobe(); err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	input, output := filepath.Join(dir, "audio.mp4"), filepath.Join(dir, "last.png")
	cmd := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "sine=duration=0.1", "-c:a", "aac", input)
	HideConsole(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("audio fixture: %s %v", out, err)
	}
	if err := ExtractLastVideoFrame(context.Background(), input, "mov", output); err == nil {
		t.Fatal("audio-only source produced a last video frame")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("missing video left an output")
	}
}

func TestExtractLastVideoFrameCancelsBothDecodingPasses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell process cancellation fixture")
	}
	for _, pass := range []string{"probe", "extract"} {
		t.Run(pass, func(t *testing.T) {
			dir := t.TempDir()
			input, output := filepath.Join(dir, "source.mp4"), filepath.Join(dir, "last.png")
			if err := os.WriteFile(input, []byte("fixture"), 0o600); err != nil {
				t.Fatal(err)
			}
			probe := filepath.Join(dir, "ffprobe")
			probeScript := "#!/bin/sh\nprintf '%s' '{\"streams\":[{\"nb_read_frames\":\"1\"}]}'\n"
			if pass == "probe" {
				probeScript = "#!/bin/sh\nexec sleep 10\n"
			}
			if err := os.WriteFile(probe, []byte(probeScript), 0o755); err != nil {
				t.Fatal(err)
			}
			encoder := filepath.Join(dir, "ffmpeg")
			if err := os.WriteFile(encoder, []byte("#!/bin/sh\nexec sleep 10\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv(FFprobePathEnv, probe)
			t.Setenv(FFmpegPathEnv, encoder)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			started := time.Now()
			if err := ExtractLastVideoFrame(ctx, input, "mov", output); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("cancellation was lost: %v", err)
			}
			if time.Since(started) > time.Second {
				t.Fatal("decoding process survived its deadline")
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatal("cancelled decode left an output")
			}
		})
	}
}
