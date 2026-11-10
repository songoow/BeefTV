package canvasworkflow

import (
	"os/exec"

	"infinite-canvas/backend/internal/mediatools"
)

// Check the runtime before saving or admitting the whole workflow, so an
// upstream generation cannot spend work before a missing media tool is found.
// Timestamp extraction does not need FFprobe and stays available independently.
func validateLastFrameRuntime(nodes []Node) error {
	for _, n := range nodes {
		if !IsLastVideoFrame(n) {
			continue
		}
		for _, tool := range []struct {
			name    string
			resolve func() (string, error)
		}{{"视频编码器", mediatools.ResolveFFmpeg}, {"视频探测器", mediatools.ResolveFFprobe}} {
			path, err := tool.resolve()
			if err == nil {
				_, err = exec.LookPath(path)
			}
			if err != nil {
				return BadRequest("节点 " + n.Title + " 的实际末帧需要可用的" + tool.name + "，请更新安装版后重试")
			}
		}
		return nil
	}
	return nil
}
