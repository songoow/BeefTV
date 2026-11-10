package task

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	localasset "infinite-canvas/backend/internal/asset"
	cw "infinite-canvas/backend/internal/canvasworkflow"
	"infinite-canvas/backend/internal/mediatools"
	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"
)

func TestCanvasMediaLastFramePersistsAndRecovers(t *testing.T) {
	ffmpeg, err := mediatools.ResolveFFmpeg()
	if err != nil {
		t.Skip(err)
	}
	if _, err := mediatools.ResolveFFprobe(); err != nil {
		t.Skip(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dir := t.TempDir()
	path := filepath.Join(dir, "source.mp4")
	cmd := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-nostdin", "-f", "lavfi", "-i", "color=c=red:s=64x48:r=25:d=0.8",
		"-vf", "drawbox=c=blue:t=fill:enable=eq(n\\,19)", "-c:v", "libx264", "-pix_fmt", "yuv420p", path)
	mediatools.HideConsole(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native fixture: %s %v", out, err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	svc, db, _ := specializedSQLite(t, nil)
	if err := db.AutoMigrate(&model.UserDailyUploadUsage{}, &model.UserUploadReservation{}); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(db)
	resources := localasset.NewService(localasset.Dependencies{
		Repository: localasset.NewRepository(repo), Blobs: localasset.NewFileStore(dir), LocalStorage: true,
		Quota: localasset.NewFileQuota(repo, func() (localasset.FileQuotaPolicy, error) {
			return localasset.FileQuotaPolicy{ResourceUploadMB: 8, GeneratedFileMB: 8, DailyUploadMB: 64, StoredFileGB: 1}, nil
		}, nil),
	})
	source, err := resources.RecoverOwned("owner", "source:0", func() (localasset.RecoveredArtifact, error) {
		return localasset.RecoveredArtifact{Kind: "video", FileName: "source.mp4", MimeType: "video/mp4", Size: int64(len(body)), Width: 64, Height: 48, DurationMs: 800, Body: bytes.NewReader(body)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The worker must derive the final frame from bytes, even when stored
	// duration metadata is stale or incomplete.
	if err := db.Model(&model.Resource{}).Where("id = ?", source.ID).Update("duration_ms", 1).Error; err != nil {
		t.Fatal(err)
	}
	admit := func(operation string, config map[string]any) *model.Task {
		t.Helper()
		node := cw.Node{ID: "frame", Role: "operation", OperationID: "video.frame", OperationVersion: 1, MediaType: "image", Config: config}
		task, err := svc.CreateCanvasMediaTask("owner", node, source.ID, operation)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Model(task).Update("status", model.TaskStatusRunning).Error; err != nil {
			t.Fatal(err)
		}
		task.Status = model.TaskStatusRunning
		return task
	}
	delivered := 0
	process := func(task *model.Task, runCtx context.Context) error {
		return ProcessCanvasMedia(runCtx, repo, resources, resources.Open, func(model.Task) error { delivered++; return nil }, nil, nil, task)
	}
	checkColor := func(task *model.Task, blue bool) string {
		t.Helper()
		stored, err := repo.Task(task.ID)
		if err != nil || stored.Status != model.TaskStatusSucceeded {
			t.Fatalf("media task did not succeed: %+v %v", stored, err)
		}
		id := cw.OutputResource(stored.ResultJSON)
		resource, reader, err := resources.Open("owner", id)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		if resource.Status != model.ResourceStatusReady || resource.Width != 64 || resource.Height != 48 {
			t.Fatalf("output is not a ready reference image: %+v", resource)
		}
		picture, err := png.Decode(reader)
		if err != nil {
			t.Fatal(err)
		}
		r, _, b, _ := picture.At(32, 24).RGBA()
		if blue && (b < 220*257 || r > 30*257) || !blue && (r < 220*257 || b > 30*257) {
			t.Fatalf("wrong frame color: red=%d blue=%d wantBlue=%v", r/257, b/257, blue)
		}
		return id
	}
	last := admit("last-frame", map[string]any{"mode": "last"})
	if err := process(last, ctx); err != nil {
		t.Fatal(err)
	}
	lastID := checkColor(last, true)
	first := admit("timestamp-frame", map[string]any{"seconds": 0.0})
	if err := process(first, ctx); err != nil {
		t.Fatal(err)
	}
	checkColor(first, false)
	stopped := admit("cancelled-frame", map[string]any{"mode": "last"})
	stopCtx, stop := context.WithCancel(ctx)
	stop()
	if err := process(stopped, stopCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("media task lost cancellation: %v", err)
	}
	stored, err := repo.Task(stopped.ID)
	if err != nil || stored.Status != model.TaskStatusRunning || stored.ResultJSON != "" {
		t.Fatalf("cancelled worker wrote a terminal result: %+v %v", stored, err)
	}
	t.Setenv(mediatools.FFmpegPathEnv, filepath.Join(dir, "missing-ffmpeg"))
	t.Setenv(mediatools.FFprobePathEnv, filepath.Join(dir, "missing-ffprobe"))
	if err := db.Model(last).Update("status", model.TaskStatusRunning).Error; err != nil {
		t.Fatal(err)
	}
	last.Status = model.TaskStatusRunning
	if err := process(last, ctx); err != nil {
		t.Fatal(err)
	}
	if id := checkColor(last, true); id != lastID {
		t.Fatalf("recovery replaced the original frame: %s != %s", id, lastID)
	}
	if delivered != 3 {
		t.Fatalf("unexpected delivery count: %d", delivered)
	}
}
