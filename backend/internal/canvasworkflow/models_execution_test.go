package canvasworkflow

import (
	"context"
	"encoding/json"
	"testing"

	"infinite-canvas/backend/internal/beeftv"
	"infinite-canvas/backend/internal/generation"
	"infinite-canvas/backend/internal/modelcatalog"
)

func TestWorkflowCustomChannelCannotRetainManagedIdentity(t *testing.T) {
	for _, selectAgain := range []bool{true, false} {
		name := "saved legacy recipe"
		if selectAgain {
			name = "explicit channel switch"
		}
		t.Run(name, func(t *testing.T) {
			host := referenceTransportModelHost(map[string]string{"video": "inline"}, "user", true, true)
			recipe := Recipe{ChannelID: "selected", Request: RecipeRequest{Model: "video", Input: map[string]any{"mode": "video", "config": map[string]any{
				"model": "video", "interfaceType": "newapi-channel-2", "baseUrl": "https://selected.invalid",
				"apiKey": "synthetic-previous", "credentialRef": beeftv.CredentialRef, "channelId": beeftv.ChannelID,
				"managedAccountId": "previous-account", "managedTokenId": "previous-token",
			}}}}
			if selectAgain {
				recipe.ChannelID = "@beeftv"
				edited, err := SelectModel(host, Node{MediaType: "video", Config: map[string]any{"modelOption": "selected::video"}}, &recipe)
				if err != nil || !edited || recipe.ChannelID != "selected" {
					t.Fatalf("channel identity change was not treated as an edit: edited=%v error=%v", edited, err)
				}
			}
			request, err := Materialize(host, "owner", "new-operation", recipe, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := generation.ResolveManagedBeefTVSecrets(request.Input, nil, func() ([]byte, error) {
				return []byte(`{"channels":[{"id":"beeftv","baseUrl":"https://managed.invalid","apiKey":"synthetic-managed"}]}`), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			config := resolved["config"].(map[string]any)
			raw, _ := json.Marshal(resolved)
			if config["apiKey"] != "current-fixture" || config["baseUrl"] != "https://selected.invalid" || beeftv.IsManagedTaskInput(string(raw)) {
				t.Fatal("selected custom channel was rerouted through the previous managed connection")
			}
			if config["managedAccountId"] != nil || config["managedTokenId"] != nil || config["channelId"] != nil {
				t.Fatal("previous connection binding survived custom credential refresh")
			}
		})
	}
}

func TestWorkflowChannelSelectionUsesChosenIdentityAtSharedEndpoint(t *testing.T) {
	for _, selected := range []string{"beeftv", "custom-b"} {
		t.Run(selected, func(t *testing.T) {
			channels := []any{}
			for _, id := range []string{"custom-a", "custom-b", "beeftv"} {
				channels = append(channels, map[string]any{
					"id": id, "enabled": true, "baseUrl": "https://shared.invalid", "apiKey": "synthetic-" + id, "secretKey": "secret-" + id,
					"headers": []any{map[string]any{"name": "X-Connection", "value": id}}, "models": []string{"video"},
					"modelProfiles": []any{map[string]any{"model": "video", "capability": "video", "protocol": "newapi-channel-2"}},
				})
			}
			host := executionCredentialHost{referenceTransportHost{snapshot: map[string]any{"channels": channels}}}
			recipe := Recipe{ChannelID: "custom-a", Request: RecipeRequest{Model: "video", Input: map[string]any{"mode": "video", "config": map[string]any{
				"model": "video", "interfaceType": "newapi-channel-2", "apiFormat": "openai", "baseUrl": "https://shared.invalid",
				"apiKey": "synthetic-custom-a", "secretKey": "secret-custom-a", "headers": []any{map[string]any{"name": "X-Connection", "value": "custom-a"}},
			}}}}
			if _, err := SelectModel(host, Node{MediaType: "video", Config: map[string]any{"modelOption": selected + "::video"}}, &recipe); err != nil {
				t.Fatal(err)
			}
			request, err := Materialize(host, "owner", "identity-proof", recipe, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := generation.ResolveManagedBeefTVSecrets(request.Input, nil, func() ([]byte, error) { return json.Marshal(host.snapshot) })
			if err != nil {
				t.Fatal(err)
			}
			input := workflowExecutionInput(t, resolved)
			if input.Config.APIKey != "synthetic-"+selected || input.Config.BaseURL != "https://shared.invalid" {
				t.Fatal("shared endpoint resolved credentials from a different channel")
			}
			if selected == "beeftv" {
				if recipe.ChannelID != "@beeftv" || input.Config.CredentialRef != beeftv.CredentialRef || input.Config.SecretKey != "" || len(input.Config.Headers) != 0 {
					t.Fatal("managed selection retained the previous custom connection")
				}
			} else if recipe.ChannelID != selected || input.Config.CredentialRef != "" || input.Config.SecretKey != "secret-custom-b" || len(input.Config.Headers) != 1 || input.Config.Headers[0].Value != "custom-b" {
				t.Fatal("custom selection retained the previous connection's authentication")
			}
		})
	}
}

type executionCredentialHost struct{ referenceTransportHost }

func (executionCredentialHost) ManagedCredential() (string, string, error) {
	return "synthetic-beeftv", "https://shared.invalid", nil
}
func (executionCredentialHost) ManagedCredentialRef() string { return beeftv.CredentialRef }

func TestWorkflowModelSwitchKeepsVideoControlsInProtocolRequest(t *testing.T) {
	for _, tc := range []struct {
		audio, watermark string
	}{
		{"true", "false"}, {"false", "true"}, {"", ""},
	} {
		t.Run("audio="+tc.audio+",watermark="+tc.watermark, func(t *testing.T) {
			host := referenceTransportModelHost(nil, "user", true, true)
			config := map[string]any{"model": "previous", "videoSeconds": "11"}
			if tc.audio != "" {
				config["videoGenerateAudio"], config["videoWatermark"] = tc.audio, tc.watermark
			}
			recipe := Recipe{Request: RecipeRequest{Input: map[string]any{"mode": "video", "config": config}}}
			if _, err := SelectModel(host, Node{MediaType: "video", Config: map[string]any{"modelOption": "selected::video"}}, &recipe); err != nil {
				t.Fatal(err)
			}
			input := workflowExecutionInput(t, recipe.Request.Input)
			request := generation.ProtocolRequestFromInput(input)
			value, explicit := request.Extra["generateAudio"]
			if request.GenerateAudio != (tc.audio == "true") || request.Watermark != (tc.watermark == "true") || explicit != (tc.audio != "") || explicit && value != (tc.audio == "true") {
				t.Fatalf("confirmed video controls changed: audio=%v watermark=%v explicitAudio=%v", request.GenerateAudio, request.Watermark, value)
			}
			if input.Config.VideoWatermark != tc.watermark || request.Extra["videoSeconds"] != "11" {
				t.Fatal("watermark presence or duration changed during model selection")
			}
		})
	}
}

func TestWorkflowModelResaveRefreshesProfileAndFreezesItForExecution(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new model"
		if existing {
			name = "same model resaved"
		}
		t.Run(name, func(t *testing.T) {
			profile := modelcatalog.DefaultModelCapabilityConfigForModel("openai-videos", "unseen-video")
			profile.Video.Resolutions, profile.Video.DefaultResolution = []string{"1080p"}, "1080p"
			row := map[string]any{"model": "agnes-video-2.5-flash", "capability": "video", "protocol": "agnes-video", "capabilityConfig": profile, "videoCapabilitiesVersion": "server-v2", "referenceMediaTransport": map[string]string{"video": "inline"}}
			host := referenceTransportHost{snapshot: map[string]any{"channels": []any{map[string]any{
				"id": "selected", "enabled": true, "baseUrl": "https://selected.invalid", "apiKey": "synthetic", "models": []string{"agnes-video-2.5-flash"}, "modelProfiles": []any{row},
			}}}}
			config := map[string]any{"model": "previous", "vquality": "720p", "seed": "17"}
			if existing {
				config["model"], config["interfaceType"], config["baseUrl"] = row["model"], row["protocol"], "https://selected.invalid"
				config["videoCapabilitiesVersion"] = "server-v1"
			}
			recipe := Recipe{Request: RecipeRequest{Input: map[string]any{"mode": "video", "config": config}}}
			node := Node{MediaType: "video", Config: map[string]any{"modelOption": "selected::agnes-video-2.5-flash"}}
			if edited, err := SelectModel(host, node, &recipe); err != nil || !edited {
				t.Fatalf("current profile not selected: edited=%v error=%v", edited, err)
			}
			if edited, err := SelectModel(host, node, &recipe); err != nil || edited {
				t.Fatalf("unchanged profile was treated as a new edit: edited=%v error=%v", edited, err)
			}
			// Later catalog changes do not rewrite a saved recipe during execution.
			profile.Video.Resolutions, profile.Video.DefaultResolution = []string{"720p"}, "720p"
			row["videoCapabilitiesVersion"] = "server-v3"
			row["referenceMediaTransport"] = map[string]string{"video": "url"}
			request, err := Materialize(host, "owner", "run", recipe, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			input := workflowExecutionInput(t, request.Input)
			if input.Config.VideoCapabilitiesVersion == nil || *input.Config.VideoCapabilitiesVersion != "server-v2" || input.Config.ReferenceMediaTransport["video"] != "inline" || request.Input["config"].(map[string]any)["seed"] != "17" {
				t.Fatal("credential refresh replaced a frozen profile or generation parameter")
			}
			if err = generation.ResolveVideoCapability(context.Background(), nil, nil, &input); err != nil {
				t.Fatal(err)
			}
			if got := generation.ProtocolRequestFromInput(input).Resolution; got != "1080p" {
				t.Fatalf("saved versioned profile did not reach the provider request: %q", got)
			}
		})
	}
}

func workflowExecutionInput(t *testing.T, value map[string]any) generation.Input {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var input generation.Input
	if err = json.Unmarshal(encoded, &input); err != nil {
		t.Fatal(err)
	}
	return input
}
