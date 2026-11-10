package canvasworkflow

// An explicit editor choice replaces the original task's frame assignment.
// Omission preserves legacy recipes, including their first/last-frame mode.
func applyVideoReferenceMode(n Node, input map[string]any) bool {
	mode, explicit := n.Config["videoEditOperation"]
	if n.OperationID != "generate.video" || !explicit {
		return false
	}
	metadata, _ := input["metadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
		input["metadata"] = metadata
	}
	for _, key := range []string{"videoEditOperation", "videoOperation", "videoStartFrameNodeId", "videoEndFrameNodeId", "videoMiddleFrameNodeId", "videoMiddleFrameTime", "videoDraftTaskId"} {
		delete(metadata, key)
	}
	if mode != "" {
		metadata["videoEditOperation"] = mode
	}
	return true
}
