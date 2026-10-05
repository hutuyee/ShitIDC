// Package marketplace 实现插件市场：从市场索引发现扩展/主题/魔方上游包，
// 下载并校验后交给各自的安装流程。
//
// 市场本身就是一个 JSON 索引（格式见 docs/marketplace.md），可以自托管：
// 把 index.json 和安装包放到任意静态服务器或对象存储上，后台把索引地址
// 指过去即可。下载走 SSRF 防护的 HTTP 客户端，支持 sha256 校验。
package marketplace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hutuyee/ShitIDC/internal/security"
)

// 包种类（kind）。
const (
	KindExtension  = "extension"   // WASM 扩展（extension.json + .wasm）
	KindTheme      = "theme"       // 主题包
	KindZJMFPlugin = "zjmf-plugin" // 魔方 server 插件（导入为 custom 供应商）
)

// DefaultIndexURL 是市场索引的默认地址（官方索引；可被后台设置覆盖）。
const DefaultIndexURL = "https://raw.githubusercontent.com/hutuyee/ShitIDC-marketplace/main/index.json"

// Item 是索引里的一条可安装项。
type Item struct {
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Author      string   `json:"author,omitempty"`
	Homepage    string   `json:"homepage,omitempty"`
	DownloadURL string   `json:"download_url"`
	SHA256      string   `json:"sha256,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Size        int64    `json:"size,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
}

// Index 是市场索引文件本身。
type Index struct {
	Version   int    `json:"version"`
	UpdatedAt string `json:"updated_at,omitempty"`
	Items     []Item `json:"items"`
	SourceURL string `json:"-"` // 读取来源，便于排查
}

// FetchIndex 拉取并解析市场索引。
func FetchIndex(ctx context.Context, indexURL string) (*Index, error) {
	indexURL = strings.TrimSpace(indexURL)
	if indexURL == "" {
		indexURL = DefaultIndexURL
	}
	client := security.SafeHTTPClient(false, 20*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, indexURL, nil)
	if err != nil {
		return nil, fmt.Errorf("索引地址无效: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("拉取市场索引失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("市场索引返回 HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("读取市场索引失败: %w", err)
	}
	var idx Index
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, fmt.Errorf("市场索引不是合法 JSON: %w", err)
	}
	if len(idx.Items) == 0 {
		return nil, errors.New("市场索引为空")
	}
	idx.SourceURL = indexURL
	return &idx, nil
}

// Find 在索引里定位一个包。
func (idx *Index) Find(kind, name string) (Item, error) {
	for _, it := range idx.Items {
		if it.Kind == kind && it.Name == name {
			return it, nil
		}
	}
	return Item{}, fmt.Errorf("市场里没有找到 %s 类型的包 %q", kind, name)
}

// ErrChecksumMismatch 表示下载内容与索引声明的 sha256 不一致。
var ErrChecksumMismatch = errors.New("下载包的 sha256 与市场索引声明不一致，已拒绝安装")

// Download 下载安装包并做 sha256 校验（item.SHA256 非空时）。
func Download(ctx context.Context, item Item) ([]byte, error) {
	if strings.TrimSpace(item.DownloadURL) == "" {
		return nil, errors.New("该条目缺少 download_url")
	}
	client := security.SafeHTTPClient(false, 60*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, item.DownloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("下载地址无效: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("下载安装包失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("下载安装包返回 HTTP %d", resp.StatusCode)
	}
	const maxPkg = 50 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPkg+1))
	if err != nil {
		return nil, fmt.Errorf("读取安装包失败: %w", err)
	}
	if int64(len(data)) > maxPkg {
		return nil, fmt.Errorf("安装包超过 %d MB 限制", maxPkg>>20)
	}
	if want := strings.ToLower(strings.TrimSpace(item.SHA256)); want != "" {
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != want {
			return nil, ErrChecksumMismatch
		}
	}
	return data, nil
}
