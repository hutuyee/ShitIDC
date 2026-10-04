// demo-logger is the reference WASM extension (Extension SDK example).
//
// Build: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o demo-logger.wasm .
// (see scripts/build-extension.sh)
//
// It subscribes to every event, logs the event name, and demonstrates the
// storage capability by counting received events in extension kv storage.
//go:build js || wasip1

package main

import (
	"unsafe"
)

// Host ABI v1 — module "shitidc". See internal/extension/host.go for the
// full contract.

//go:wasmimport shitidc log
func hostLog(ptr unsafe.Pointer, length uint32)

//go:wasmimport shitidc random
func hostRandom(outPtr unsafe.Pointer, outLen uint32)

//go:wasmimport shitidc kv_get
func hostKVGet(keyPtr unsafe.Pointer, keyLen uint32, outPtr unsafe.Pointer, outMax uint32) uint32

//go:wasmimport shitidc kv_set
func hostKVSet(keyPtr unsafe.Pointer, keyLen uint32, valPtr unsafe.Pointer, valLen uint32) uint32

// keepAlive pins every buffer handed to the host so the Go GC cannot reclaim
// memory the host still references between calls.
var keepAlive [][]byte

func main() {}

// extAlloc reserves a scratch buffer for host -> guest data (ABI v1).
//
//go:wasmexport ext_alloc
func extAlloc(length uint32) unsafe.Pointer {
	buf := make([]byte, length)
	keepAlive = append(keepAlive, buf)
	if len(buf) == 0 {
		return unsafe.Pointer(&keepAlive[0])
	}
	return unsafe.Pointer(&buf[0])
}

// extOnEvent is the event entry point; return 0 on success (ABI v1).
//
//go:wasmexport ext_on_event
func extOnEvent(namePtr unsafe.Pointer, nameLen uint32, _ unsafe.Pointer, payloadLen uint32) int32 {
	name := readString(namePtr, nameLen)
	logString("demo-logger saw event: " + name + " (payload " + itoa(uint64(payloadLen)) + " bytes)")
	count := readCount() + 1
	writeCount(count)
	logString("demo-logger total events handled: " + itoa(count))
	return 0
}

func readString(ptr unsafe.Pointer, length uint32) string {
	if length == 0 {
		return ""
	}
	return string(unsafe.Slice((*byte)(ptr), length))
}

func readCount() uint64 {
	key := []byte("event_count")
	buf := make([]byte, 8)
	n := hostKVGet(unsafe.Pointer(&key[0]), uint32(len(key)), unsafe.Pointer(&buf[0]), uint32(len(buf)))
	if n != 8 {
		return 0
	}
	var v uint64
	for _, b := range buf {
		v = v<<8 | uint64(b)
	}
	return v
}

func writeCount(v uint64) {
	key := []byte("event_count")
	buf := make([]byte, 8)
	for i := 0; i < 8; i++ {
		buf[7-i] = byte(v >> (8 * i))
	}
	hostKVSet(unsafe.Pointer(&key[0]), uint32(len(key)), unsafe.Pointer(&buf[0]), uint32(len(buf)))
}

func logString(s string) {
	if s == "" {
		return
	}
	b := []byte(s)
	hostLog(unsafe.Pointer(&b[0]), uint32(len(b)))
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}
