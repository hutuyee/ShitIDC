package extension

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/hutuyee/ShitIDC/internal/store"
)

// The demo extension is prebuilt with GOOS=wasip1 (scripts/build-extension.sh).
const demoWasmPath = "../../extensions/demo-logger/demo-logger.wasm"

func loadDemoManifest() Manifest {
	return Manifest{
		Name: "demo-logger", Version: "1.0.0", Entry: "demo-logger.wasm",
		Description: "test",
		Permissions: []string{"log", "storage"},
		Events:      []string{},
	}
}

func TestValidateManifest(t *testing.T) {
	m := Manifest{Name: "ok-ext", Version: "1.0.0", Entry: "ext.wasm", Permissions: []string{"log", "storage"}}
	if err := m.Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	bad := []Manifest{
		{Name: "Bad Name", Version: "1", Entry: "e.wasm"},
		{Name: "ok", Version: "1", Entry: "e.exe"},
		{Name: "ok", Version: "1", Entry: "e.wasm", Permissions: []string{"root"}},
	}
	for i, b := range bad {
		if err := b.Validate(); err == nil {
			t.Fatalf("bad manifest %d accepted", i)
		}
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("demo wasm not built yet: %v", err)
	}
	return b
}

func TestLoadAndDispatchDemoExtension(t *testing.T) {
	wasm := mustRead(t, demoWasmPath)
	h := NewHost(nil) // storage-backed imports degrade gracefully with nil store
	defer h.Close()

	inst, err := h.Load(context.Background(), loadDemoManifest(), wasm)
	if err != nil {
		t.Fatalf("load demo extension: %v", err)
	}
	h.Dispatch(context.Background(), "order.paid", []byte(`{"order_id":"O123","amount_cents":5000}`))

	logs := inst.Logs
	if len(logs) < 2 {
		t.Fatalf("expected log lines from extension, got %d: %v", len(logs), logs)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "demo-logger saw event: order.paid") {
		t.Fatalf("event log missing: %v", logs)
	}
	if !strings.Contains(joined, "total events handled: 1") {
		t.Fatalf("kv counter missing: %v", logs)
	}
	// Dispatch a second event: the counter must increment through storage.
	h.Dispatch(context.Background(), "user.login", []byte(`{}`))
	joined = strings.Join(inst.Logs, "\n")
	if !strings.Contains(joined, "total events handled: 2") {
		t.Fatalf("counter did not increment: %v", inst.Logs)
	}
}

func TestLoadRejectsNonWasm(t *testing.T) {
	h := NewHost(nil)
	defer h.Close()
	if _, err := h.Load(context.Background(), loadDemoManifest(), []byte("not a wasm module")); err == nil {
		t.Fatal("non-wasm bytes accepted")
	}
}

func TestNilStoreKVPathFailsSoft(t *testing.T) {
	// Without a store the kv imports degrade to -1 and the extension still
	// runs — used by tests and non-persistent runtimes.
	h := NewHost(nil)
	defer h.Close()
	inst, err := h.Load(context.Background(), loadDemoManifest(), mustRead(t, demoWasmPath))
	if err != nil {
		t.Fatal(err)
	}
	h.Dispatch(context.Background(), "user.login", []byte(`{}`))
	if len(inst.Logs) == 0 {
		t.Fatal("extension produced no logs")
	}
	_ = store.ErrNotFound
}
