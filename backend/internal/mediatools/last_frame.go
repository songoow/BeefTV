package mediatools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// ExtractLastVideoFrame writes the last decoded frame of v:0 as a PNG. Both
// passes decode the same local video stream; no duration, seek estimate or
// whole-video frame buffer is used. The caller owns the output's temp directory
// and the execution deadline, including both decoding passes.
func ExtractLastVideoFrame(ctx context.Context, sourcePath, format, outputPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Stat(sourcePath)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("媒体文件无法读取")
	}
	if format != "mov" && format != "matroska" {
		return fmt.Errorf("此视频格式暂不支持实际末帧提取")
	}
	probe, err := ResolveFFprobe()
	if err != nil {
		return err
	}
	encoder, err := ResolveFFmpeg()
	if err != nil {
		return err
	}
	count := exec.CommandContext(ctx, probe, "-v", "error", "-protocol_whitelist", "file,pipe",
		"-f", format, "-select_streams", "v:0", "-count_frames",
		"-show_entries", "stream=nb_read_frames", "-of", "json", sourcePath)
	HideConsole(count)
	raw, err := count.Output()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("无法解码视频帧，请检查源文件是否完整：%w", err)
	}
	var result struct {
		Streams []struct {
			Frames string `json:"nb_read_frames"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || len(result.Streams) != 1 {
		return fmt.Errorf("源文件没有可解码的视频流")
	}
	frames, err := strconv.ParseInt(result.Streams[0].Frames, 10, 64)
	if err != nil || frames < 1 {
		return fmt.Errorf("源视频没有可解码的画面")
	}
	// select runs before output frame-rate synchronization. Mapping v:0 in both
	// passes also avoids choosing a larger alternate stream or an audio tail.
	extract := exec.CommandContext(ctx, encoder, "-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-protocol_whitelist", "file,pipe", "-f", format, "-i", sourcePath,
		"-map", "0:v:0", "-vf", fmt.Sprintf("select=eq(n\\,%d)", frames-1),
		"-fps_mode", "passthrough", "-frames:v", "1", "-an", "-sn", "-dn", "-c:v", "png", "-f", "image2", outputPath)
	HideConsole(extract)
	err = extract.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("实际末帧提取失败：%w", err)
	}
	if info, err := os.Stat(outputPath); err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("视频解码未产生实际末帧")
	}
	return nil
}
