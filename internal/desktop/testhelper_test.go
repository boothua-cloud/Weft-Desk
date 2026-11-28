package desktop

import (
	"database/sql"
	"testing"

	"github.com/boothua/weft/internal/store"
)

// newTestStore 用内存库，不碰真实配置目录。
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.NewWithDB(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}
