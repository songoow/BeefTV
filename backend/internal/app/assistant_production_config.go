package app

import (
	"encoding/json"
	"strings"

	"infinite-canvas/backend/internal/modelcatalog"
	"infinite-canvas/backend/internal/operations"
	"infinite-canvas/backend/internal/providerpreset"
)

func (s *Service) assistantGenerationConfig(target operations.GenerationTarget) (map[string]any, error) {
	body, err := s.ReadLocalModelConfig()
	if err != nil {
		return nil, err
	}
	channelID, modelID := modelcatalog.SplitModelKey(target.ModelKey)
	var snapshot map[string]any
	if err = json.Unmarshal(body, &snapshot); err != nil {
		return nil, err
	}
	var config map[string]any
	for _, profile := range modelcatalog.ParseLocalChannelModels(body) {
		if profile.ChannelID == channelID && profile.Model == modelID && profile.Enabled {
			config = modelcatalog.LocalModelExecutionConfig(snapshot, profile)
			if target.Kind == "video" {
				if _, protocol, ok := providerpreset.HostedBeefTVVideoProfile(profile.BaseURL, modelID); ok {
					config["interfaceType"] = protocol
				}
			}
			break
		}
	}
	if config == nil {
		return nil, operations.PreconditionFailed("generation_model_unavailable", "制作模型已不可用", nil)
	}
	for key, value := range target.Parameters {
		switch key {
		case "seconds":
			key = "videoSeconds"
		case "generateAudio":
			key = "videoGenerateAudio"
		case "watermark":
			key = "videoWatermark"
		case "videoStartFrameNodeId", "videoEndFrameNodeId", "videoMiddleFrameNodeId", "referenceMediaTransport":
			continue
		}
		config[key] = value
	}
	if strings.TrimSpace(stringValue(config["count"])) == "" {
		config["count"] = "1"
	}
	return config, nil
}
