// Package archutil provides hardened zip extraction for theme and extension
// packages (§20/§44): Zip Slip guards, size and file-count caps and a
// filename whitelist. Nothing is written to disk here — callers receive the
// extracted files as bytes and decide where they land.
package archutil

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

var AllowedPackageExts = []string{".wasm", ".json", ".css", ".js", ".webp", ".png", ".jpg", ".jpeg", ".svg", ".txt", ".md", ".html", ".woff", ".woff2", ".ttf"}

// SafeReadZip extracts a zip from r. Limits:
//   - maxBytes: total uncompressed size cap
//   - maxFiles: entry count cap
//   - only files with a whitelisted extension are accepted
//   - entry names must be clean, relative and stay inside the root (Zip Slip)
func SafeReadZip(r io.Reader, maxBytes int64, maxFiles int) (map[string][]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取压缩包失败: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("压缩包超过 %d MB 限制", maxBytes>>20)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("不是有效的 zip 压缩包")
	}
	if len(zr.File) > maxFiles {
		return nil, fmt.Errorf("压缩包内文件数超过 %d 限制", maxFiles)
	}
	out := map[string][]byte{}
	var total int64
	for _, f := range zr.File {
		name := f.Name
		// Zip Slip: reject absolute paths, drive letters and traversal.
		if strings.Contains(name, "\\") || strings.Contains(name, "..") || strings.HasPrefix(name, "/") || path.IsAbs(name) {
			return nil, fmt.Errorf("压缩包内存在非法路径: %s", name)
		}
		clean := path.Clean(name)
		if clean == "." || strings.HasPrefix(clean, "..") {
			return nil, fmt.Errorf("压缩包内存在非法路径: %s", name)
		}
		if f.FileInfo().IsDir() {
			continue
		}
		ext := strings.ToLower(path.Ext(clean))
		allowed := false
		for _, a := range AllowedPackageExts {
			if ext == a {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("压缩包含不允许的文件类型: %s", name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("读取 %s 失败", name)
		}
		content, err := io.ReadAll(io.LimitReader(rc, maxBytes+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("读取 %s 失败", name)
		}
		total += int64(len(content))
		if total > maxBytes {
			return nil, errors.New("解压后总大小超过限制（疑似压缩炸弹）")
		}
		out[clean] = content
	}
	if len(out) == 0 {
		return nil, errors.New("压缩包为空")
	}
	return out, nil
}
