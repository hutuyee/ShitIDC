package zjmfimport

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// 魔方插件包的上限：经典模块最大的也就几百 KB，给 10MB 已足够宽裕。
const (
	maxZipBytes  int64 = 10 << 20
	maxZipFiles        = 200
	maxFileBytes       = 2 << 20
)

// FromZipReader 读取一个魔方插件 zip（如 plugins/server/BtVirtualHost.zip 或
// 手工打包的 <标识>/<标识>.php）并转换。
func FromZipReader(r io.Reader) (*ParseResult, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxZipBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取压缩包失败: %w", err)
	}
	if int64(len(data)) > maxZipBytes {
		return nil, fmt.Errorf("压缩包超过 %d MB 限制", maxZipBytes>>20)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("不是有效的 zip 压缩包")
	}
	if len(zr.File) > maxZipFiles {
		return nil, fmt.Errorf("压缩包内文件数超过 %d 限制", maxZipFiles)
	}
	files := map[string][]byte{}
	for _, f := range zr.File {
		name := f.Name
		if strings.Contains(name, "\\") || strings.Contains(name, "..") || strings.HasPrefix(name, "/") {
			return nil, fmt.Errorf("压缩包内存在非法路径: %s", name)
		}
		if f.FileInfo().IsDir() {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(name), ".php") {
			continue // 模板/版本文件等对本转换没有价值
		}
		if f.UncompressedSize64 > maxFileBytes {
			return nil, fmt.Errorf("压缩包内 %s 超过单文件大小限制", name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("读取 %s 失败: %w", name, err)
		}
		content, err := io.ReadAll(io.LimitReader(rc, maxFileBytes+1))
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("读取 %s 失败: %w", name, err)
		}
		files[name] = content
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("压缩包里没有 PHP 文件——魔方插件导入只处理 server 模块（PHP）")
	}
	return FromFiles("", files)
}

// FromDir 读取一个已解包的插件目录并转换（CLI 与测试用）。
func FromDir(dir string) (*ParseResult, error) {
	files := map[string][]byte{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(strings.ToLower(info.Name()), ".php") {
			return nil
		}
		if info.Size() > maxFileBytes {
			return fmt.Errorf("%s 超过单文件大小限制", path)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = content
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("目录 %s 里没有 PHP 文件", dir)
	}
	return FromFiles("", files)
}
