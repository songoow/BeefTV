package canvasworkflow

import (
	"encoding/json"
	"strings"

	"infinite-canvas/backend/internal/beeftv"
	"infinite-canvas/backend/internal/generation"
	"infinite-canvas/backend/internal/modelcatalog"
)

// Resolve a public model selection against current owned channel configuration;
// never accept endpoints or credentials supplied in a workflow definition.
func SelectModel(host CompileHost, n Node, recipe *Recipe) (bool, error) {
	option := stringValue(n.Config["modelOption"])
	if option == "" {
		return false, nil
	}
	snapshot, err := host.ProviderConfig()
	if err != nil {
		return false, err
	}
	raw, _ := json.Marshal(snapshot)
	for _, candidate := range modelcatalog.ParseLocalChannelModels(raw) {
		if !candidate.Enabled || generation.UpscaleMediaType(candidate.Model) != "" || candidate.Capability != n.MediaType || candidate.Protocol == "" || candidate.ChannelID+"::"+candidate.Model != option {
			continue
		}
		old, _ := recipe.Request.Input["config"].(map[string]any)
		previousConfig, _ := json.Marshal(old)
		current := modelcatalog.LocalModelExecutionConfig(snapshot, candidate)
		managed := beeftv.IsManagedChannel(candidate.ChannelID, stringValue(current["credentialRef"]), candidate.BaseURL)
		wasManaged := beeftv.IsManagedChannel(stringValue(old["channelId"]), stringValue(old["credentialRef"]), stringValue(old["baseUrl"]))
		recipe.ChannelID = candidate.ChannelID
		if managed {
			recipe.ChannelID = "@beeftv"
		}
		sameModel := managed == wasManaged && stringValue(old["model"]) == candidate.Model && stringValue(old["interfaceType"]) == candidate.Protocol && strings.TrimRight(stringValue(old["baseUrl"]), "/") == strings.TrimRight(candidate.BaseURL, "/")
		config := old
		if !sameModel {
			config = current
			// The template pins a connection identity; execution reads its secrets.
			for _, key := range []string{"apiKey", "secretKey", "headers"} {
				delete(config, key)
			}
			for _, key := range []string{"size", "quality", "vquality", "videoSeconds", "seed", "videoGenerateAudio", "videoWatermark", "count"} {
				if v, ok := old[key]; ok {
					config[key] = v
				}
			}
			recipe.Request.Model = candidate.Model
			recipe.Request.Provider = ""
			recipe.Request.LogicalModelID = ""
		} else {
			// An explicit save refreshes the complete profile and its version
			// together; execution only refreshes the connection credentials.
			modelcatalog.ApplyLocalModelProfile(config, candidate)
		}
		currentConfig, _ := json.Marshal(config)
		recipe.Request.Input["config"] = config
		return !sameModel || string(previousConfig) != string(currentConfig), nil
	}
	return false, BadRequest("请选择当前已启用、类型匹配的模型")
}
