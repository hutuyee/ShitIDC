// Package extension is the extension runtime (第十/二十四阶段).
//
// Extensions are WASM (wasip1) modules executed by wazero with a
// capability-gated host ABI — never native plugins, so a crashing extension
// cannot take down the core. Permissions come from the uploaded
// extension.json manifest and are enforced inside every host import.
//
// ABI v1 (module "shitidc", guest exports prefixed ext_):
//
//	guest export  ext_alloc(len u32) -> ptr                 — scratch buffer
//	guest export  ext_on_event(name_ptr, name_len, payload_ptr, payload_len) -> i32
//	guest import  log(ptr, len)                             — always granted
//	guest import  random(out_ptr, out_len)
//	guest import  kv_get(key_ptr, key_len, out_ptr, out_max) -> actual_len  — permission storage
//	guest import  kv_set(key_ptr, key_len, val_ptr, val_len) -> i32         — permission storage
//	guest import  http_fetch(url_ptr, url_len, out_ptr, out_max) -> actual_len — permission http
//
// Return conventions: actual_len >= 0 for success, -1 for failure. Results
// larger than the guest buffer are truncated, never overflowed.
package extension

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"github.com/hutuyee/ShitIDC/internal/security"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// Known permissions (§23 扩展权限): anything else in a manifest is rejected.
var KnownPermissions = map[string]bool{"log": true, "storage": true, "http": true}

const (
	maxWasmBytes   = 20 << 20 // 20 MB module cap
	maxKVValue     = 1 << 20
	maxHTTPBody    = 1 << 20
	extCallTimeout = 15 * time.Second
)

// Manifest is the extension.json inside an uploaded package.
type Manifest struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Entry       string   `json:"entry"`
	Permissions []string `json:"permissions"`
	Events      []string `json:"events"` // empty = subscribe to all events
}

// Validate checks manifest invariants and the permission whitelist.
func (m *Manifest) Validate() error {
	m.Name = strings.TrimSpace(m.Name)
	if m.Name == "" || len(m.Name) > 64 {
		return errors.New("扩展 manifest 缺少有效 name")
	}
	for _, r := range m.Name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return errors.New("扩展 name 仅允许小写字母、数字、- 和 _")
		}
	}
	if strings.TrimSpace(m.Version) == "" {
		return errors.New("扩展 manifest 缺少 version")
	}
	if strings.TrimSpace(m.Entry) == "" || !strings.HasSuffix(m.Entry, ".wasm") {
		return errors.New("扩展 manifest entry 必须是 .wasm 文件")
	}
	for _, p := range m.Permissions {
		if !KnownPermissions[p] {
			return fmt.Errorf("未知扩展权限 %q（允许: log, storage, http）", p)
		}
	}
	return nil
}

// ParseManifest decodes and validates an extension.json body.
func ParseManifest(raw []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("extension.json 解析失败: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Instance is one loaded extension module bound to its manifest.
type Instance struct {
	Manifest Manifest
	module   api.Module
	logMu    sync.Mutex
	Logs     []string
}

// Host owns the wazero runtime and the set of loaded extensions.
type Host struct {
	runtime wazero.Runtime
	mu      sync.RWMutex
	loaded  map[string]*Instance // by extension name
	store   *store.Store
	// memKV backs the storage capability when no store is attached (tests,
	// ephemeral runtimes); with a store, kv is database-backed.
	memKV map[string][]byte
}

func NewHost(st *store.Store) *Host {
	return &Host{runtime: wazero.NewRuntime(context.Background()), loaded: map[string]*Instance{}, store: st, memKV: map[string][]byte{}}
}

func (h *Host) kvGet(ctx context.Context, extension, key string) ([]byte, bool, error) {
	if h.store == nil {
		h.mu.Lock()
		defer h.mu.Unlock()
		v, ok := h.memKV[extension+"\x00"+key]
		return v, ok, nil
	}
	return h.store.ExtStorageGet(ctx, extension, key)
}

func (h *Host) kvSet(ctx context.Context, extension, key string, value []byte) error {
	if h.store == nil {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.memKV[extension+"\x00"+key] = append([]byte(nil), value...)
		return nil
	}
	return h.store.ExtStorageSet(ctx, extension, key, value)
}

// Close releases the runtime and every module.
func (h *Host) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	_ = h.runtime.Close(context.Background())
	h.loaded = map[string]*Instance{}
}

// Load compiles and instantiates a module under its manifest, replacing any
// previous version of the same extension name.
func (h *Host) Load(ctx context.Context, m Manifest, wasmBytes []byte) (*Instance, error) {
	if len(wasmBytes) == 0 || len(wasmBytes) > maxWasmBytes {
		return nil, fmt.Errorf("wasm 模块大小必须介于 1 字节和 %d 字节之间", maxWasmBytes)
	}
	if len(wasmBytes) < 4 || string(wasmBytes[:4]) != "\x00asm" {
		return nil, errors.New("不是有效的 WASM 模块（缺少 \\0asm 魔数）")
	}
	granted := map[string]bool{}
	for _, p := range m.Permissions {
		granted[p] = true
	}
	inst := &Instance{Manifest: m}

	_, err := wasi_snapshot_preview1.NewBuilder(h.runtime).Instantiate(ctx)
	if err != nil {
		return nil, fmt.Errorf("wasi 实例化失败: %w", err)
	}
	_, err = h.runtime.NewHostModuleBuilder("shitidc").
		NewFunctionBuilder().WithFunc(h.logFn(inst)).Export("log").
		NewFunctionBuilder().WithFunc(h.randomFn()).Export("random").
		NewFunctionBuilder().WithFunc(h.kvGetFn(inst, granted)).Export("kv_get").
		NewFunctionBuilder().WithFunc(h.kvSetFn(inst, granted)).Export("kv_set").
		NewFunctionBuilder().WithFunc(h.httpFetchFn(inst, granted)).Export("http_fetch").
		Instantiate(ctx)
	if err != nil {
		return nil, fmt.Errorf("宿主 ABI 实例化失败: %w", err)
	}
	mod, err := h.runtime.Instantiate(ctx, wasmBytes)
	if err != nil {
		return nil, fmt.Errorf("扩展模块实例化失败: %w", err)
	}
	// wasip1 reactor modules (go build -buildmode=c-shared) export
	// _initialize, which boots the embedded Go runtime; it must run before
	// any other exported function is called.
	if init := mod.ExportedFunction("_initialize"); init != nil {
		if _, err := init.Call(ctx); err != nil {
			_ = mod.Close(ctx)
			return nil, fmt.Errorf("扩展初始化失败: %w", err)
		}
	}
	if mod.ExportedFunction("ext_on_event") == nil || mod.ExportedFunction("ext_alloc") == nil {
		_ = mod.Close(ctx)
		return nil, errors.New("扩展缺少 ext_on_event / ext_alloc 导出（请按 ABI v1 实现）")
	}
	inst.module = mod

	h.mu.Lock()
	if old, ok := h.loaded[m.Name]; ok {
		_ = old.module.Close(ctx)
	}
	h.loaded[m.Name] = inst
	h.mu.Unlock()
	return inst, nil
}

// Instance returns a loaded instance by name (for log inspection).
func (h *Host) Instance(name string) *Instance {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.loaded[name]
}

// Unload removes one extension module by name.
func (h *Host) Unload(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if inst, ok := h.loaded[name]; ok {
		_ = inst.module.Close(context.Background())
		delete(h.loaded, name)
	}
}

// Loaded lists loaded extension names.
func (h *Host) Loaded() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]string, 0, len(h.loaded))
	for name := range h.loaded {
		out = append(out, name)
	}
	return out
}

// Dispatch delivers one committed event to every loaded extension that
// subscribed to it. Extension failures are logged and swallowed: a bad
// extension must never break the business flow that emitted the event.
func (h *Host) Dispatch(ctx context.Context, eventName string, payload []byte) {
	h.mu.RLock()
	instances := make([]*Instance, 0, len(h.loaded))
	for _, inst := range h.loaded {
		subscribed := len(inst.Manifest.Events) == 0
		for _, e := range inst.Manifest.Events {
			if e == eventName {
				subscribed = true
				break
			}
		}
		if subscribed {
			instances = append(instances, inst)
		}
	}
	h.mu.RUnlock()
	for _, inst := range instances {
		if err := inst.dispatchOne(ctx, eventName, payload); err != nil {
			inst.appendLog(fmt.Sprintf("[error] event %s: %v", eventName, err))
		}
	}
}

func (inst *Instance) dispatchOne(ctx context.Context, eventName string, payload []byte) error {
	dctx, cancel := context.WithTimeout(ctx, extCallTimeout)
	defer cancel()
	name := []byte(eventName)
	namePtr, err := inst.writeBuffer(dctx, name)
	if err != nil {
		return err
	}
	payloadPtr, err := inst.writeBuffer(dctx, payload)
	if err != nil {
		return err
	}
	fn := inst.module.ExportedFunction("ext_on_event")
	results, err := fn.Call(dctx, uint64(namePtr), uint64(len(name)), uint64(payloadPtr), uint64(len(payload)))
	if err != nil {
		return err
	}
	if len(results) > 0 && int32(results[0]) != 0 {
		return fmt.Errorf("ext_on_event returned %d", int32(results[0]))
	}
	return nil
}

// writeBuffer calls ext_alloc and copies data into guest memory.
func (inst *Instance) writeBuffer(ctx context.Context, data []byte) (uint32, error) {
	if len(data) == 0 {
		data = []byte{0}
	}
	results, err := inst.module.ExportedFunction("ext_alloc").Call(ctx, uint64(len(data)))
	if err != nil {
		return 0, fmt.Errorf("ext_alloc: %w", err)
	}
	if len(results) == 0 {
		return 0, errors.New("ext_alloc returned nothing")
	}
	ptr := uint32(results[0])
	if !inst.module.Memory().Write(ptr, data) {
		return 0, errors.New("ext_alloc pointer out of range")
	}
	return ptr, nil
}

func (inst *Instance) appendLog(line string) {
	inst.logMu.Lock()
	if len(inst.Logs) > 500 {
		inst.Logs = inst.Logs[1:]
	}
	inst.Logs = append(inst.Logs, time.Now().UTC().Format(time.RFC3339)+" "+line)
	inst.logMu.Unlock()
}

// ---- host ABI implementations (capability-gated) ----

func (h *Host) logFn(inst *Instance) func(context.Context, api.Module, uint32, uint32) {
	return func(_ context.Context, m api.Module, ptr, length uint32) {
		buf, ok := m.Memory().Read(ptr, length)
		if !ok {
			return
		}
		inst.appendLog(string(buf))
	}
}

func (h *Host) randomFn() func(context.Context, api.Module, uint32, uint32) {
	return func(_ context.Context, m api.Module, outPtr, outLen uint32) {
		if outLen == 0 || outLen > 1<<20 {
			return
		}
		buf, ok := m.Memory().Read(outPtr, outLen)
		if !ok {
			return
		}
		_, _ = cryptorand.Read(buf)
	}
}

// readArg extracts (ptr,len) guest data.
func readArg(m api.Module, ptr, length uint32) ([]byte, bool) {
	if length == 0 {
		return []byte{}, true
	}
	return m.Memory().Read(ptr, length)
}

func (h *Host) kvGetFn(inst *Instance, granted map[string]bool) func(context.Context, api.Module, uint32, uint32, uint32, uint32) uint32 {
	return func(ctx context.Context, m api.Module, keyPtr, keyLen, outPtr, outMax uint32) uint32 {
		if !granted["storage"] {
			return errNo
		}
		key, ok := readArg(m, keyPtr, keyLen)
		if !ok {
			return errNo
		}
		value, found, err := h.kvGet(ctx, inst.Manifest.Name, string(key))
		if err != nil || !found {
			return errNo
		}
		n := uint32(len(value))
		if n > outMax {
			n = outMax
		}
		if !m.Memory().Write(outPtr, value[:n]) {
			return errNo
		}
		return n
	}
}

func (h *Host) kvSetFn(inst *Instance, granted map[string]bool) func(context.Context, api.Module, uint32, uint32, uint32, uint32) uint32 {
	return func(ctx context.Context, m api.Module, keyPtr, keyLen, valPtr, valLen uint32) uint32 {
		if !granted["storage"] {
			return errNo
		}
		key, ok := readArg(m, keyPtr, keyLen)
		if !ok || keyLen == 0 || valLen > maxKVValue {
			return errNo
		}
		val, ok := readArg(m, valPtr, valLen)
		if !ok {
			return errNo
		}
		if err := h.kvSet(ctx, inst.Manifest.Name, string(key), val); err != nil {
			return errNo
		}
		return 0
	}
}

func (h *Host) httpFetchFn(inst *Instance, granted map[string]bool) func(context.Context, api.Module, uint32, uint32, uint32, uint32) uint32 {
	return func(ctx context.Context, m api.Module, urlPtr, urlLen, outPtr, outMax uint32) uint32 {
		if !granted["http"] {
			return errNo
		}
		rawURL, ok := readArg(m, urlPtr, urlLen)
		if !ok || urlLen == 0 {
			return errNo
		}
		// SSRF guard (§42): https only, dial-time IP checks, no redirects to
		// blocked networks.
		client := security.SafeHTTPClient(false, 10*time.Second)
		req, err := newRequestWithContext(ctx, string(rawURL))
		if err != nil {
			return errNo
		}
		resp, err := client.Do(req)
		if err != nil {
			return errNo
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return errNo
		}
		buf := make([]byte, outMax)
		n, _ := readBody(resp, buf)
		if !m.Memory().Write(outPtr, buf[:n]) {
			return errNo
		}
		return uint32(n)
	}
}

const errNo = ^uint32(0) // -1

// newRequestWithContext validates the URL against the SSRF policy (§42)
// before building an https GET request.
func newRequestWithContext(ctx context.Context, rawURL string) (*http.Request, error) {
	if err := security.ValidateOutboundURL(rawURL, false); err != nil {
		return nil, err
	}
	return http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
}

// readBody fills buf (the guest-provided output window) without ever
// exceeding it; short bodies are fine.
func readBody(resp *http.Response, buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	n, err := io.ReadFull(resp.Body, buf)
	if err == io.ErrUnexpectedEOF || err == io.EOF {
		err = nil
	}
	return n, err
}
