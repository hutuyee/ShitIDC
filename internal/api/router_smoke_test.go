package api

import (
	"testing"

	"github.com/hutuyee/ShitIDC/internal/config"
)

// Guards against gin route conflicts (which panic at registration time).
func TestNewRouterRegistersWithoutConflict(t *testing.T) {
	a := &App{Cfg: config.Config{}}
	r := NewRouter(a)
	if len(r.Routes()) < 80 {
		t.Fatalf("unexpectedly few routes registered: %d", len(r.Routes()))
	}
}
