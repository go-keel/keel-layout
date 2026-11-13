// Example file: delete the whole file after forking

package data

import (
	"context"
	"errors"
	"log/slog"

	"gorm.io/gorm"

	"github.com/go-keel/keel-layout/internal/biz"
	"github.com/go-keel/keel/core/kdb"
)

type todoRepo struct {
	db *kdb.DB
}

var _ biz.TodoRepo = (*todoRepo)(nil)

// NewTodoRepo returns the interface type, not the concrete type.
func NewTodoRepo(db *kdb.DB) biz.TodoRepo {
	return &todoRepo{db: db}
}

func (r *todoRepo) Create(ctx context.Context, t *biz.Todo) (*biz.Todo, error) {
	m := newTodo(t)
	if err := r.db.WithContext(ctx).Create(m).Error; err != nil {
		slog.ErrorContext(ctx, "failed to create todo", "error", err)
		return nil, errors.Join(biz.ErrUnexpectedError, err)
	}
	return m.toBiz(), nil
}

func (r *todoRepo) FindByID(ctx context.Context, id int64) (*biz.Todo, error) {
	var m todo
	err := r.db.WithContext(ctx).First(&m, id).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		// Expected condition → domain error; upper layers never see the driver
		return nil, biz.ErrTodoNotFound
	case err != nil:
		slog.ErrorContext(ctx, "failed to find todo", "error", err, "id", id)
		return nil, errors.Join(biz.ErrUnexpectedError, err)
	}
	return m.toBiz(), nil
}

func (r *todoRepo) List(ctx context.Context, opts biz.ListOptions) ([]*biz.Todo, int64, error) {
	// The sort is an enum and accepts only the values biz lists; column names belong to data (update_time → updated_at).
	var order string
	switch opts.Sort {
	case biz.TodoSortDefault: // relies only on the id ascending order below
	case biz.TodoSortUpdateTimeDesc:
		order = "updated_at DESC"
	default:
		return nil, 0, biz.ErrInvalidArgument
	}

	q := r.db.WithContext(ctx).Model(&todo{})

	// ListOptions → SQL translation happens in the data layer and nowhere else
	if opts.Completed != nil {
		q = q.Where("completed = ?", *opts.Completed)
	}

	// Count after filtering and before sorting and paging: total means "how many match in all",
	// regardless of which rows this page takes.
	var total int64
	if err := q.Count(&total).Error; err != nil {
		slog.ErrorContext(ctx, "failed to count todos", "error", err)
		return nil, 0, errors.Join(biz.ErrUnexpectedError, err)
	}

	if order != "" {
		q = q.Order(order)
	}
	q = q.Order("id ASC") // default order and tie-breaker for the others: rows with equal values keep a fixed order, so paging neither repeats nor skips

	var ms []todo
	if err := q.Offset(opts.Offset).Limit(opts.Limit).Find(&ms).Error; err != nil {
		slog.ErrorContext(ctx, "failed to list todos", "error", err)
		return nil, 0, errors.Join(biz.ErrUnexpectedError, err)
	}
	todos := make([]*biz.Todo, 0, len(ms))
	for i := range ms {
		todos = append(todos, ms[i].toBiz())
	}
	return todos, total, nil
}

// updatableColumns maps biz field names to storage column names. Column names belong to data (the PO shape is its
// private business), field names belong to biz; each side has its own vocabulary, and the mapping lives here.
var updatableColumns = map[string]string{
	biz.FieldTitle:     "title",
	biz.FieldContent:   "content",
	biz.FieldCompleted: "completed",
}

func (r *todoRepo) Update(ctx context.Context, t *biz.Todo, fields []string) (*biz.Todo, error) {
	cols := make([]string, 0, len(fields))
	for _, f := range fields {
		col, ok := updatableColumns[f]
		if !ok {
			// biz has already applied its allowlist; reaching here means the two field sets drifted apart.
			slog.ErrorContext(ctx, "unknown update field", "field", f, "id", t.ID)
			return nil, biz.ErrInvalidArgument
		}
		cols = append(cols, col)
	}

	res := r.db.WithContext(ctx).Model(&todo{}).
		Where("id = ?", t.ID).
		// Select allowlist: only these columns go into SET, and zero values (completed=false) are written too.
		Select(cols).
		Updates(newTodo(t))
	if res.Error != nil {
		slog.ErrorContext(ctx, "failed to update todo", "error", res.Error, "id", t.ID)
		return nil, errors.Join(biz.ErrUnexpectedError, res.Error)
	}
	if res.RowsAffected == 0 {
		return nil, biz.ErrTodoNotFound
	}
	return r.FindByID(ctx, t.ID)
}

func (r *todoRepo) Delete(ctx context.Context, id int64) error {
	res := r.db.WithContext(ctx).Delete(&todo{}, id)
	if res.Error != nil {
		slog.ErrorContext(ctx, "failed to delete todo", "error", res.Error, "id", id)
		return errors.Join(biz.ErrUnexpectedError, res.Error)
	}
	if res.RowsAffected == 0 {
		return biz.ErrTodoNotFound
	}
	return nil
}
