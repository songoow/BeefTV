package app

import (
	"encoding/json"
	"strings"

	"infinite-canvas/backend/internal/beeftv"
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
	var snapshot struct {
		Channels []struct {
			ID            string `json:"id"`
			BaseURL       string `json:"baseUrl"`
			APIKey        string `json:"apiKey"`
			CredentialRef string `json:"credentialRef"`
			APIFormat     string `json:"apiFormat"`
			InterfaceType string `json:"interfaceType"`
		} `json:"channels"`
	}
	if err = json.Unmarshal(body, &snapshot); err != nil {
		return nil, err
	}
	config := map[string]any{"model": modelID}
	for _, channel := range snapshot.Channels {
		if channel.ID == channelID {
			config["baseUrl"], config["apiKey"], config["apiFormat"] = channel.BaseURL, channel.APIKey, channel.APIFormat
			config["interfaceType"] = channel.InterfaceType
			if channel.CredentialRef != "" {
				config["credentialRef"] = channel.CredentialRef
			}
			if channel.ID == beeftv.ChannelID {
				config["channelId"] = channel.ID
			}
			break
		}
	}
	found := false
	for _, profile := range modelcatalog.ParseLocalChannelModels(body) {
		if profile.ChannelID == channelID && profile.Model == modelID && profile.Enabled {
			found = true
			config["interfaceType"] = firstNonEmpty(profile.Protocol, stringValue(config["interfaceType"]))
			if len(profile.ReferenceMediaTransport) > 0 {
				config["referenceMediaTransport"] = profile.ReferenceMediaTransport
			}
			if profile.CapabilityConfig != nil {
				config["capabilityConfig"] = profile.CapabilityConfig
			}
			if target.Kind == "video" {
				if _, protocol, ok := providerpreset.HostedBeefTVVideoProfile(profile.BaseURL, modelID); ok {
					config["interfaceType"] = protocol
				}
			}
			break
		}
	}
	if !found {
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
