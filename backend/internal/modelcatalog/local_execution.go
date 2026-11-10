package modelcatalog

import "infinite-canvas/backend/internal/model"

// LocalModelExecutionConfig projects one selected local model into a private
// task config. Connection secrets stay out of the public model catalog; callers
// must select an enabled model from this same snapshot before using this map.
func LocalModelExecutionConfig(snapshot map[string]any, item LocalChannelModel) map[string]any {
	config := map[string]any{}
	channels, _ := snapshot["channels"].([]any)
	for _, raw := range channels {
		channel, _ := raw.(map[string]any)
		if channel["id"] != item.ChannelID {
			continue
		}
		for _, key := range []string{"apiKey", "secretKey", "headers", "apiFormat", "interfaceType", "credentialRef", "referenceAssetOrigin"} {
			if value, ok := channel[key]; ok {
				config[key] = value
			}
		}
		break
	}
	// Local channel IDs are not database system-channel IDs. The managed
	// credential reference is sufficient for admission to resolve that identity.
	if item.ChannelID == managedChannelID {
		config["credentialRef"] = managedCredentialRef
	}
	config["model"], config["baseUrl"] = item.Model, item.BaseURL
	if item.Protocol != "" {
		config["interfaceType"] = item.Protocol
	}
	ApplyLocalModelProfile(config, item)
	return config
}

// ApplyLocalModelProfile refreshes model-owned settings on an explicit save
// without replacing the recipe's generation parameters or connection secrets.
func ApplyLocalModelProfile(config map[string]any, item LocalChannelModel) {
	config["apiFormat"] = ChannelAPIFormatForProtocol(admissionString(config["apiFormat"]), model.ChannelInterfaceType(item.Protocol))
	for _, key := range []string{"capabilityConfig", "imageCapabilitiesVersion", "videoCapabilitiesVersion", "referenceMediaTransport"} {
		delete(config, key)
	}
	if item.CapabilityConfig != nil {
		config["capabilityConfig"] = item.CapabilityConfig
	}
	if item.ImageCapabilitiesVersion != nil {
		config["imageCapabilitiesVersion"] = *item.ImageCapabilitiesVersion
	}
	if item.VideoCapabilitiesVersion != nil {
		config["videoCapabilitiesVersion"] = *item.VideoCapabilitiesVersion
	}
	if len(item.ReferenceMediaTransport) > 0 {
		config["referenceMediaTransport"] = item.ReferenceMediaTransport
	}
}
