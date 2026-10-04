package config

import (
	"path/filepath"
)

// StorageConfig is the seam for future file storage (ticket attachments,
// theme packages). Everything still lives on the local filesystem; object
// storage backends plug in here later.
type StorageConfig struct {
	Dir string
}

// ThemeDir is where uploaded theme packages are installed. It defaults to
// the repository's static-served ./themes directory.
func (c StorageConfig) ThemeDir() string {
	if c.Dir == "" || c.Dir == "." {
		return "./themes"
	}
	return filepath.Join(c.Dir, "themes")
}

func loadStorage() StorageConfig {
	return StorageConfig{
		Dir: env("DATA_DIR", "./data"),
	}
}
