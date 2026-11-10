package canvasworkflow

import (
	"encoding/json"
	"reflect"
	"testing"
)

type referenceTransportHost struct {
	CompileHost
	snapshot map[string]any
}

func (h referenceTransportHost) ProviderConfig() (map[string]any, error) { return h.snapshot, nil }
func (h referenceTransportHost) LocalMode() bool                         { return true }

func TestSelectModelInheritsCurrentReferenceTransport(t *testing.T) {
	transport := map[string]string{"image": "inline", "video": "url", "audio": "auto"}
	for _, tc := range []struct {
		name     string
		previous any
		current  map[string]string
		scope    string
		newModel bool
		wantEdit bool
	}{
		{name: "new model", current: transport, newModel: true, wantEdit: true},
		{name: "same settings", previous: transport, current: transport},
		{name: "changed profile", previous: map[string]string{"image": "url", "video": "url"}, current: transport, wantEdit: true},
		{name: "reset to protocol default", previous: transport, wantEdit: true},
		{name: "system policy", previous: transport, current: transport, scope: "system", wantEdit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := referenceTransportModelHost(tc.current, tc.scope, true, true)
			config := map[string]any{"model": "video", "interfaceType": "newapi-channel-2", "apiFormat": "openai", "baseUrl": "https://selected.invalid/", "seed": "17", "referenceAssetOrigin": "frozen-origin"}
			if tc.newModel {
				config["model"] = "old"
			}
			if tc.previous != nil {
				// Persisted recipes decode maps as map[string]any rather than the
				// typed map returned by the local model catalog.
				body, _ := json.Marshal(tc.previous)
				var previous any
				if err := json.Unmarshal(body, &previous); err != nil {
					t.Fatal(err)
				}
				config["referenceMediaTransport"] = previous
			}
			recipe := Recipe{Request: RecipeRequest{Model: "video", Input: map[string]any{"config": config}}}
			node := Node{MediaType: "video", Config: map[string]any{"modelOption": "selected::video", "referenceMediaTransport": map[string]string{"video": "inline"}}}
			edited, err := SelectModel(host, node, &recipe)
			if err != nil || edited != tc.wantEdit || recipe.ChannelID != "selected" {
				t.Fatalf("select model: edited=%v channel=%q err=%v", edited, recipe.ChannelID, err)
			}
			selected := recipe.Request.Input["config"].(map[string]any)
			want := tc.current
			if tc.scope == "system" {
				want = nil
			}
			if len(want) == 0 {
				if _, exists := selected["referenceMediaTransport"]; exists {
					t.Fatal("stale transport override survived the model selection")
				}
			} else if !reflect.DeepEqual(selected["referenceMediaTransport"], want) {
				t.Fatalf("wrong profile transport: %#v", selected["referenceMediaTransport"])
			}
			if selected["seed"] != "17" || (!tc.newModel && selected["referenceAssetOrigin"] != "frozen-origin") {
				t.Fatal("selecting transport changed unrelated recipe parameters")
			}
		})
	}
}

func TestSelectModelRejectsUnavailableTransportProfile(t *testing.T) {
	for _, tc := range []struct {
		name              string
		enabled, selected bool
	}{
		{"disabled channel", false, true},
		{"deselected model", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := referenceTransportModelHost(map[string]string{"video": "inline"}, "user", tc.enabled, tc.selected)
			recipe := Recipe{Request: RecipeRequest{Input: map[string]any{"config": map[string]any{}}}}
			if _, err := SelectModel(host, Node{MediaType: "video", Config: map[string]any{"modelOption": "selected::video"}}, &recipe); err == nil {
				t.Fatal("unavailable model profile was selected")
			}
		})
	}
}

func TestWorkflowCredentialRefreshKeepsFrozenReferenceTransport(t *testing.T) {
	host := referenceTransportModelHost(map[string]string{"video": "url"}, "user", true, true)
	frozen := map[string]string{"image": "inline", "video": "inline"}
	config := map[string]any{"model": "video", "interfaceType": "newapi-channel-2", "apiKey": "previous-fixture", "referenceMediaTransport": frozen}
	if err := RefreshCredentials(host, map[string]any{"config": config}, "selected"); err != nil {
		t.Fatal(err)
	}
	if config["apiKey"] != "current-fixture" || !reflect.DeepEqual(config["referenceMediaTransport"], frozen) {
		t.Fatal("credential refresh did not preserve the recipe's frozen transport")
	}
}

func referenceTransportModelHost(transport map[string]string, scope string, enabled, selected bool) referenceTransportHost {
	models := []string{"sibling"}
	if selected {
		models = append(models, "video")
	}
	inline := map[string]string{"image": "inline", "video": "inline"}
	return referenceTransportHost{snapshot: map[string]any{"channels": []any{
		map[string]any{"id": "other", "enabled": true, "models": []string{"video"}, "modelProfiles": []any{map[string]any{"model": "video", "capability": "video", "protocol": "newapi-channel-2", "referenceMediaTransport": inline}}},
		map[string]any{"id": "selected", "scope": scope, "enabled": enabled, "apiKey": "current-fixture", "baseUrl": "https://selected.invalid", "models": models, "referenceMediaTransport": inline, "modelProfiles": []any{
			map[string]any{"model": "sibling", "capability": "video", "protocol": "newapi-channel-2", "referenceMediaTransport": inline},
			map[string]any{"model": "video", "capability": "video", "protocol": "newapi-channel-2", "referenceMediaTransport": transport},
		}},
	}}}
}
