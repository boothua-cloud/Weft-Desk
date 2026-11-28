package engine

import (
	"os"
	"path/filepath"
	"time"

	"github.com/boothua/weft/internal/model"
)

func ConnectedMarkPath(c model.Config) string {
	dir, err := StateDir(c)
	if err != nil {
		return ""
	}
	return filepath.Join(dir, ".connected")
}

func MarkConnected(c model.Config) {
	if p := ConnectedMarkPath(c); p != "" {
		_ = os.WriteFile(p, []byte(time.Now().Format(time.RFC3339)), 0o600)
	}
}
