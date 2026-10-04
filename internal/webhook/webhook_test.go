package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

func TestSignPayloadIsStableHMAC(t *testing.T) {
	body := []byte(`{"event":"order.paid"}`)
	got := SignPayload("whsec_test", 1700000000, body)
	want := "t=1700000000,v1=" + hexStr(hmacSHA256([]byte("whsec_test"), []byte("1700000000."+string(body))))
	if got != want {
		t.Fatalf("signature mismatch:\n got %s\nwant %s", got, want)
	}
	// A different secret or body must produce a different signature.
	if SignPayload("other", 1700000000, body) == got {
		t.Fatal("different secret must change signature")
	}
	if SignPayload("whsec_test", 1700000001, body) == got {
		t.Fatal("different timestamp must change signature")
	}
}

func TestEnvelopeJSONShape(t *testing.T) {
	env := Envelope{ID: "d1", Event: "order.paid", OccurredAt: time.Unix(1700000000, 0).UTC(), Data: map[string]any{"order_id": "o1"}}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "event", "occurred_at", "data"} {
		if _, ok := back[key]; !ok {
			t.Fatalf("envelope missing %q: %s", key, b)
		}
	}
}

func hmacSHA256(key, msg []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(msg)
	return m.Sum(nil)
}

func hexStr(b []byte) string { return hex.EncodeToString(b) }
