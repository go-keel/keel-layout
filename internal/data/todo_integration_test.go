//go:build integration

// Example file: delete the whole file after forking

package data

import (
	"context"
	"errors"
	"testing"

	"github.com/go-keel/keel-layout/internal/biz"
)

// Runs under make integration on a fresh MySQL database built from migrations/*/0_schema.sql
// (testdb_integration_test.go). A data-layer test for a new resource follows this shape.
func TestTodoRepo(t *testing.T) {
	ctx := context.Background()
	repo := NewTodoRepo(openTestDB(t))

	a, err := repo.Create(ctx, &biz.Todo{Title: "a", Content: "x"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := repo.Create(ctx, &biz.Todo{Title: "b", Completed: true}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.FindByID(ctx, a.ID)
	if err != nil || got.Title != "a" || got.Content != "x" || got.Completed {
		t.Fatalf("FindByID = %+v, %v", got, err)
	}

	done := true
	rows, total, err := repo.List(ctx, biz.ListOptions{Completed: &done, Limit: 10})
	if err != nil || total != 1 || len(rows) != 1 || rows[0].Title != "b" {
		t.Fatalf("List completed = %v rows, total %d, err %v; want only b", len(rows), total, err)
	}
	rows, total, err = repo.List(ctx, biz.ListOptions{Limit: 1})
	if err != nil || total != 2 || len(rows) != 1 {
		t.Fatalf("List page = %d rows, total %d, err %v; want 1 row of 2", len(rows), total, err)
	}

	// Only the listed field is written: the content stays.
	up, err := repo.Update(ctx, &biz.Todo{ID: a.ID, Title: "a2", Content: "ignored"}, []string{"title"})
	if err != nil || up.Title != "a2" || up.Content != "x" {
		t.Fatalf("Update = %+v, %v; want title a2 and content unchanged", up, err)
	}

	if err := repo.Delete(ctx, a.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.FindByID(ctx, a.ID); !errors.Is(err, biz.ErrTodoNotFound) {
		t.Errorf("FindByID after delete: got %v, want ErrTodoNotFound", err)
	}
	if err := repo.Delete(ctx, a.ID); !errors.Is(err, biz.ErrTodoNotFound) {
		t.Errorf("Delete twice: got %v, want ErrTodoNotFound", err)
	}
}
