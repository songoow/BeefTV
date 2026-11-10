package task

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	localasset "infinite-canvas/backend/internal/asset"
	cw "infinite-canvas/backend/internal/canvasworkflow"
	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"
)

func mediaLifecycleFixture(t *testing.T, config map[string]any) (*repository.Repository, *localasset.Service, *model.Task) {
	t.Helper()
	svc, db, _ := specializedSQLite(t, nil)
	repo := repository.New(db)
	source := model.Resource{ID: "source", UserID: "owner", Kind: "video", Status: model.ResourceStatusReady, MimeType: "video/mp4"}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	node := cw.Node{ID: "frame", Role: "operation", OperationID: "video.frame", OperationVersion: 1, MediaType: "image", Config: config}
	if _, err := svc.CreateCanvasMediaTask("owner", node, source.ID, "media-run"); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimNextTask("worker", time.Minute)
	if err != nil || claimed == nil {
		t.Fatalf("claim: task=%+v err=%v", claimed, err)
	}
	resources := localasset.NewService(localasset.Dependencies{Repository: localasset.NewRepository(repo), Blobs: localasset.NewFileStore(t.TempDir()), LocalStorage: true})
	return repo, resources, claimed
}

func TestCanvasMediaDeadlineFinalizesFailureWithoutAutomaticReclaim(t *testing.T) {
	for _, mode := range []string{"last", "timestamp", ""} {
		t.Run(mode, func(t *testing.T) {
			config := map[string]any{"seconds": 0.0}
			if mode != "" {
				config["mode"] = mode
			}
			repo, resources, claimed := mediaLifecycleFixture(t, config)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			open := func(string, string) (*model.Resource, io.ReadCloser, error) {
				<-ctx.Done()
				return nil, nil, ctx.Err()
			}
			if err := ProcessCanvasMedia(ctx, repo, resources, open, nil, nil, nil, claimed); err != nil {
				t.Fatal(err)
			}
			stored, err := repo.Task(claimed.ID)
			if err != nil || stored.Status != model.TaskStatusFailed || stored.Stage != "处理超时" || stored.CompletedAt == nil || stored.Error != TaskTimeoutMessage(claimed.Type) || stored.ResultJSON != "" {
				t.Fatalf("timeout did not persist a failed task: %+v err=%v", stored, err)
			}
			if stored.LeaseOwner != "" || stored.LeaseExpiresAt != nil {
				t.Fatal("timeout left an active lease")
			}
			if again, err := repo.ClaimNextTask("next-worker", time.Minute); err != nil || again != nil {
				t.Fatalf("timed-out task was automatically reclaimed: %+v err=%v", again, err)
			}
		})
	}
}

func TestCanvasMediaCancellationPreservesUserAndLeaseState(t *testing.T) {
	for _, cause := range []string{"user", "lease"} {
		t.Run(cause, func(t *testing.T) {
			repo, resources, claimed := mediaLifecycleFixture(t, map[string]any{"mode": "last"})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			open := func(string, string) (*model.Resource, io.ReadCloser, error) {
				if cause == "user" {
					if done, err := repo.CancelTaskIfStatus(claimed.UserID, claimed.ID, model.TaskStatusRunning, time.Now()); err != nil || !done {
						t.Fatalf("cancel: done=%t err=%v", done, err)
					}
				} else {
					reclaimMediaTask(t, repo, claimed.ID)
				}
				cancel()
				return nil, nil, ctx.Err()
			}
			if err := ProcessCanvasMedia(ctx, repo, resources, open, nil, nil, nil, claimed); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation changed: %v", err)
			}
			stored, err := repo.Task(claimed.ID)
			if err != nil {
				t.Fatal(err)
			}
			if cause == "user" && stored.Status != model.TaskStatusCancelled || cause == "lease" && (stored.Status != model.TaskStatusRunning || stored.LeaseOwner != "next-worker") {
				t.Fatalf("cancelled worker overwrote current task state: %+v", stored)
			}
		})
	}
}

func TestCanvasMediaDeadlineCannotOverwriteExpiredOrReassignedLease(t *testing.T) {
	for _, reassigned := range []bool{false, true} {
		repo, resources, claimed := mediaLifecycleFixture(t, map[string]any{"mode": "last"})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		open := func(string, string) (*model.Resource, io.ReadCloser, error) {
			if reassigned {
				reclaimMediaTask(t, repo, claimed.ID)
			} else if err := repo.DB().Model(&model.Task{}).Where("id = ?", claimed.ID).Update("lease_expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
				t.Fatal(err)
			}
			<-ctx.Done()
			return nil, nil, ctx.Err()
		}
		if err := ProcessCanvasMedia(ctx, repo, resources, open, nil, nil, nil, claimed); !errors.Is(err, repository.ErrTaskStateConflict) {
			t.Fatalf("stale timeout was not fenced: %v", err)
		}
		stored, err := repo.Task(claimed.ID)
		if err != nil || stored.Status != model.TaskStatusRunning || stored.CompletedAt != nil || stored.Error != "" {
			t.Fatalf("stale timeout changed the task: %+v err=%v", stored, err)
		}
		if reassigned && stored.LeaseOwner != "next-worker" {
			t.Fatal("stale timeout replaced the new lease")
		}
	}
}

func reclaimMediaTask(t *testing.T, repo *repository.Repository, id string) {
	t.Helper()
	if err := repo.DB().Model(&model.Task{}).Where("id = ?", id).Update("lease_expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if next, err := repo.ClaimNextTask("next-worker", time.Minute); err != nil || next == nil || next.ID != id {
		t.Fatalf("reclaim: task=%+v err=%v", next, err)
	}
}
