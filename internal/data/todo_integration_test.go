//go:build integration

// Example file: delete the whole file after forking

package data

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm"

	"github.com/go-keel/keel-layout/internal/biz"
	"github.com/go-keel/keel-layout/internal/testdb"
	"github.com/go-keel/keel/core/kdb"
)

// Runs under make integration once per dialect, each on a fresh database built from migrations/*/<dialect>/0_schema.sql
// (internal/testdb). A data-layer test for a new resource follows this shape.
func TestTodoRepo(t *testing.T) {
	testdb.ForEachDB(t, nil, testTodoRepo)
}

func testTodoRepo(t *testing.T, db *gorm.DB) {
	ctx := context.Background()
	repo := NewTodoRepo(&kdb.DB{DB: db})

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
	rows, total, err := repo.List(ctx, biz.ListTodoOptions{Completed: &done, Limit: 10})
	if err != nil || total != 1 || len(rows) != 1 || rows[0].Title != "b" {
		t.Fatalf("List completed = %v rows, total %d, err %v; want only b", len(rows), total, err)
	}
	rows, total, err = repo.List(ctx, biz.ListTodoOptions{Limit: 1})
	if err != nil || total != 2 || len(rows) != 1 {
		t.Fatalf("List page = %d rows, total %d, err %v; want 1 row of 2", len(rows), total, err)
	}

	// Only the listed field is written: the content stays.
	up, err := repo.Update(ctx, &biz.Todo{ID: a.ID, Title: "a2", Content: "ignored"}, []string{"title"})
	if err != nil || up.Title != "a2" || up.Content != "x" {
		t.Fatalf("Update = %+v, %v; want title a2 and content unchanged", up, err)
	}
	// Writing the value a row already has is still an update of an existing row, on every dialect.
	if _, err := repo.Update(ctx, &biz.Todo{ID: a.ID, Title: "a2"}, []string{"title"}); err != nil {
		t.Fatalf("Update to the same value: %v; want success", err)
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
