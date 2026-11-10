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
		recipe.ChannelID = candidate.ChannelID
		if beeftv.IsManagedChannel(candidate.ChannelID, "", candidate.BaseURL) {
			recipe.ChannelID = "@beeftv"
		}
		sameModel := stringValue(old["model"]) == candidate.Model && stringValue(old["interfaceType"]) == candidate.Protocol && strings.TrimRight(stringValue(old["baseUrl"]), "/") == strings.TrimRight(candidate.BaseURL, "/")
		config := old
		if !sameModel {
			config = map[string]any{"model": candidate.Model, "interfaceType": candidate.Protocol, "baseUrl": candidate.BaseURL, "capabilityConfig": candidate.CapabilityConfig}
			for _, key := range []string{"size", "quality", "vquality", "videoSeconds", "seed", "generateAudio", "watermark", "count"} {
				if v, ok := old[key]; ok {
					config[key] = v
				}
			}
			recipe.Request.Model = candidate.Model
			recipe.Request.Provider = ""
			recipe.Request.LogicalModelID = ""
		}
		// Re-saving the same model must pick up its current transport settings;
		// credential refresh at execution time leaves this recipe value frozen.
		previousTransport, _ := json.Marshal(config["referenceMediaTransport"])
		delete(config, "referenceMediaTransport")
		if len(candidate.ReferenceMediaTransport) > 0 {
			config["referenceMediaTransport"] = candidate.ReferenceMediaTransport
		}
		currentTransport, _ := json.Marshal(config["referenceMediaTransport"])
		recipe.Request.Input["config"] = config
		return !sameModel || string(previousTransport) != string(currentTransport), nil
	}
	return false, BadRequest("请选择当前已启用、类型匹配的模型")
}
