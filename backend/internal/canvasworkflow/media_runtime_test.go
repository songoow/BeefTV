package canvasworkflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"infinite-canvas/backend/internal/mediatools"
	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"
)

type mediaRuntimeRecipes struct{ Recipes }

func (mediaRuntimeRecipes) Compile(user string, d Definition, existing *model.CanvasWorkflow, seal bool) (Compiled, error) {
	compiled, err := Compile(nil, user, d, existing)
	return Compiled{Cipher: "{}", Presented: d, References: compiled.References}, err
}

type mediaRuntimeRunHost struct {
	RunHost
	generated int
}

func (*mediaRuntimeRunHost) Draining() bool { return false }
func (*mediaRuntimeRunHost) ReferenceIDs(string) (map[string]bool, error) {
	return map[string]bool{}, nil
}
func (h *mediaRuntimeRunHost) Generate(string, string, string, Node, map[string]Node, map[string]*Step) (*model.Task, error) {
	h.generated++
	return nil, fmt.Errorf("unexpected generation submission")
}

func mediaRuntimeStore(t *testing.T) (*repository.Repository, *Templates, *Runs, *mediaRuntimeRunHost) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Admission checks executable paths without running native binaries.
	t.Setenv(mediatools.FFmpegPathEnv, executable)
	t.Setenv(mediatools.FFprobePathEnv, executable)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "runtime.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err = db.AutoMigrate(&model.CanvasWorkflow{}, &model.CanvasWorkflowRevision{}, &model.CanvasWorkflowRun{}, &model.CanvasWorkflowResource{}); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(db)
	mu, loop := &sync.Mutex{}, false
	templates := NewTemplates(repo, mediaRuntimeRecipes{}, mu)
	host := &mediaRuntimeRunHost{}
	return repo, templates, NewRuns(repo, templates, host, mu, &loop), host
}

func mediaRuntimeDefinition(mode string, generated bool) Definition {
	source := Node{ID: "source", Title: "Video input", Role: "input", MediaType: "video"}
	if generated {
		source.Role, source.OperationID, source.OperationVersion = "operation", "generate.video", 1
		source.Config = map[string]any{"modelOption": "configured::video-model"}
	}
	config := map[string]any{"seconds": 0.0}
	if mode != "" {
		config["mode"] = mode
	}
	return Definition{Version: 1, Nodes: []Node{
		source,
		{ID: "frame", Title: "Continuity frame", Role: "operation", OperationID: "video.frame", OperationVersion: 1, MediaType: "image", Config: config},
	}, Edges: []Edge{{From: "source", To: "frame", FromPort: "output", ToPort: "video"}}, OutputNodeIDs: []string{"frame"}}
}

func TestLastFrameRuntimeCheckedWhenSaving(t *testing.T) {
	_, templates, _, _ := mediaRuntimeStore(t)
	missing := filepath.Join(t.TempDir(), "missing-ffprobe")
	t.Setenv(mediatools.FFprobePathEnv, missing)
	if _, err := templates.Create("owner", CreateRequest{Name: "Last frame", Definition: mediaRuntimeDefinition("last", false)}); err == nil || !strings.Contains(err.Error(), "实际末帧") {
		t.Fatalf("missing probe allowed a last-frame template to save: %v", err)
	}
	for _, mode := range []string{"", "timestamp"} {
		if _, err := templates.Create("owner", CreateRequest{Name: "Timestamp frame", Definition: mediaRuntimeDefinition(mode, false)}); err != nil {
			t.Fatalf("timestamp mode %q unexpectedly needs FFprobe: %v", mode, err)
		}
	}
	// A configured directory or non-executable file must not pass merely because
	// ResolveFFprobe accepts the configured path. No binary is executed here.
	paths := []string{t.TempDir()}
	if runtime.GOOS != "windows" {
		plain := filepath.Join(t.TempDir(), "not-executable")
		if err := os.WriteFile(plain, []byte("not an executable"), 0600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, plain)
	}
	for _, path := range paths {
		t.Setenv(mediatools.FFprobePathEnv, path)
		if _, err := templates.Prepare("owner", mediaRuntimeDefinition("last", false), "", 0); err == nil {
			t.Fatal("unusable configured probe was accepted")
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(mediatools.FFprobePathEnv, executable)
	if _, err = templates.Create("owner", CreateRequest{Name: "Available last frame", Definition: mediaRuntimeDefinition("last", false)}); err != nil {
		t.Fatalf("executable probe path rejected: %v", err)
	}
	t.Setenv(mediatools.FFmpegPathEnv, missing)
	if _, err = templates.Prepare("owner", mediaRuntimeDefinition("last", false), "", 0); err == nil || !strings.Contains(err.Error(), "实际末帧") {
		t.Fatalf("last-frame draft accepted a missing encoder: %v", err)
	}
}

func TestLastFrameRuntimeCheckedBeforeRunAdmission(t *testing.T) {
	repo, _, runs, host := mediaRuntimeStore(t)
	for _, mode := range []string{"last", "timestamp"} {
		definition, err := json.Marshal(mediaRuntimeDefinition(mode, true))
		if err != nil {
			t.Fatal(err)
		}
		// Simulate an already saved template: tools can disappear after saving.
		if err = repo.DB().Create(&model.CanvasWorkflow{ID: mode, UserID: "owner", Name: mode, Revision: 1, DefinitionJSON: string(definition), RecipeCipher: "{}"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	missing := filepath.Join(t.TempDir(), "missing-media-tool")
	req := RunRequest{ClientOperationID: "last-run", TemplateRevision: 1}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, missingTool := range []string{mediatools.FFprobePathEnv, mediatools.FFmpegPathEnv} {
		t.Setenv(mediatools.FFmpegPathEnv, executable)
		t.Setenv(mediatools.FFprobePathEnv, executable)
		t.Setenv(missingTool, missing)
		if _, err := runs.Start("owner", "last", req); err == nil || !strings.Contains(err.Error(), "实际末帧") {
			t.Fatalf("run with unavailable last-frame tool %s admitted: %v", missingTool, err)
		}
	}
	var count int64
	if err := repo.DB().Model(&model.CanvasWorkflowRun{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed preflight persisted a runnable workflow: count=%d error=%v", count, err)
	}
	runs.AdvanceAll(context.Background())
	if host.generated != 0 {
		t.Fatal("model generation was submitted despite missing downstream probe")
	}
	t.Setenv(mediatools.FFmpegPathEnv, executable)
	t.Setenv(mediatools.FFprobePathEnv, missing)
	if _, err := runs.Start("owner", "timestamp", RunRequest{ClientOperationID: "timestamp-run", TemplateRevision: 1}); err != nil {
		t.Fatalf("timestamp workflow could not start without FFprobe: %v", err)
	}
	t.Setenv(mediatools.FFprobePathEnv, executable)
	accepted, err := runs.Start("owner", "last", req)
	if err != nil || accepted.Status != "queued" {
		t.Fatalf("repaired runtime did not admit the original request: %+v %v", accepted, err)
	}
	t.Setenv(mediatools.FFprobePathEnv, missing)
	replayed, err := runs.Start("owner", "last", req)
	if err != nil || replayed.ID != accepted.ID {
		t.Fatalf("receipt replay was blocked or duplicated by runtime changes: %+v %v", replayed, err)
	}
}
