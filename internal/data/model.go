package data

import (
	"time"

	"github.com/go-keel/keel-layout/internal/biz"
)

// Private GORM models (POs) live in this file, each followed by its TableName, toBiz and new<Resource>; beyond 5,
// split them by domain into {domain}_model.go. A PO is named after its table in lowerCamelCase without a Model
// suffix; the lowercase start lets the compiler keep POs inside the data package, which is also why this package
// never uses a table name as a variable name.

// Example: delete after forking (through "Example end")

// todo is the persistent object of Todo: the storage shape, private to the data layer.
type todo struct {
	ID        int64 `gorm:"primaryKey;autoIncrement"`
	Title     string
	Content   string
	Completed bool      `gorm:"default:false"`
	CreatedAt time.Time `gorm:"autoCreateTime"`
	UpdatedAt time.Time `gorm:"autoUpdateTime"`
}

func (todo) TableName() string { return "todo" }

// newTodo converts DO → PO (write direction).
func newTodo(t *biz.Todo) *todo {
	return &todo{
		ID:        t.ID,
		Title:     t.Title,
		Content:   t.Content,
		Completed: t.Completed,
	}
}

// toBiz converts PO → DO (read direction).
func (m *todo) toBiz() *biz.Todo {
	return &biz.Todo{
		ID:         m.ID,
		Title:      m.Title,
		Content:    m.Content,
		Completed:  m.Completed,
		CreateTime: m.CreatedAt,
		UpdateTime: m.UpdatedAt,
	}
}

// Example end
