// Package theme manages the on-disk theme packages (第九/二十阶段): the
// themes/ directory holds one folder per theme with theme.json +
// variables.css + assets, served statically by the API. Uploads are validated
// (manifest, Zip Slip, file whitelist) before anything lands on disk.
package theme

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hutuyee/ShitIDC/internal/archutil"
)

// Package is the theme.json contract — the Theme SDK for designers:
// drop one folder with this manifest and a variables.css into themes/.
type Package struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Author      string `json:"author"`
	Description string `json:"description"`
	Engine      string `json:"engine"` // "1" = CSS variable contract
}

// ValidID reports whether a theme id is safe to use as a directory name.
var ValidID = func(id string) bool {
	if id == "" || len(id) > 48 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// List reads every theme.json under dir.
func List(dir string) ([]Package, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Package{}, nil
		}
		return nil, err
	}
	out := []Package{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name(), "theme.json"))
		if err != nil {
			continue // folder without a manifest is not a theme
		}
		var p Package
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.Name(), "variables.css")); err == nil {
			p.Engine = orOne(p.Engine)
		}
		out = append(out, p)
	}
	return out, nil
}

func orOne(v string) string {
	if v == "" {
		return "1"
	}
	return v
}

// Install extracts a theme zip into dir/<id>/ after validating the manifest,
// file whitelist and size caps (§20 主题安全: 静态资源 only).
func Install(dir string, zipBytes []byte) (*Package, error) {
	files, err := archutil.SafeReadZip(strings.NewReader(string(zipBytes)), 20<<20, 300)
	if err != nil {
		return nil, err
	}
	manifestRaw, ok := files["theme.json"]
	if !ok {
		return nil, errors.New("主题包缺少 theme.json")
	}
	var p Package
	if err := json.Unmarshal(manifestRaw, &p); err != nil {
		return nil, fmt.Errorf("theme.json 解析失败: %w", err)
	}
	if !ValidID(p.ID) {
		return nil, errors.New("theme.json 的 id 仅允许字母数字与 - _")
	}
	if _, ok := files["variables.css"]; !ok {
		return nil, errors.New("主题包缺少 variables.css（CSS 变量契约）")
	}
	target := filepath.Join(dir, p.ID)
	// Defense in depth: the join must stay inside the themes root.
	if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dir)+string(os.PathSeparator)) {
		return nil, errors.New("非法主题目录")
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return nil, err
	}
	for name, content := range files {
		if strings.Contains(name, "..") {
			return nil, fmt.Errorf("非法文件路径 %s", name)
		}
		if err := os.WriteFile(filepath.Join(target, filepath.FromSlash(name)), content, 0o644); err != nil {
			return nil, err
		}
	}
	return &p, nil
}

// Delete removes one theme folder; the active theme is guarded by the caller.
func Delete(dir, id string) error {
	if !ValidID(id) {
		return errors.New("非法主题 ID")
	}
	if id == "default" {
		return errors.New("默认主题不可删除")
	}
	target := filepath.Join(dir, id)
	if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dir)+string(os.PathSeparator)) {
		return errors.New("非法主题目录")
	}
	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		return os.ErrNotExist
	}
	return os.RemoveAll(target)
}
