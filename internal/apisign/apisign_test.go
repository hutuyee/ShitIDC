package apisign

import (
	"crypto/md5"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// phpReferenceSign is a literal transcription of the PHP the 魔方 modules use:
//
//	$data['time']=$time; $data['random']=$random; $data['token']=$token;
//	sort($data, SORT_STRING); return strtoupper(md5(implode($data)));
func phpReferenceSign(timestamp int64, random int64, token string) string {
	data := []string{strconv.FormatInt(timestamp, 10), strconv.FormatInt(random, 10), token}
	sort.Strings(data) // sort($data, SORT_STRING) sorts values, not keys
	return strings.ToUpper(hex.EncodeToString(func() []byte {
		sum := md5.Sum([]byte(strings.Join(data, "")))
		return sum[:]
	}()))
}

func TestSignMatchesPHPReference(t *testing.T) {
	cases := []struct {
		ts     int64
		random int64
		token  string
	}{
		{1791082385, 123456789, "abcdef0123456789"},
		{1791082385, 7, "0"},
		{1, 999999999999, "TOKEN"},
		// Values whose lexicographic order differs from their numeric order:
		// "10" < "9" as strings, which is exactly where a naive sort breaks.
		{9, 9, "z"},
		{1700000000, 2147483647, "x9y8z7"},
	}
	for _, tc := range cases {
		got := Sign(tc.ts, tc.random, tc.token)
		want := phpReferenceSign(tc.ts, tc.random, tc.token)
		if got != want {
			t.Fatalf("Sign(%d,%d,%q) = %s, want PHP reference %s", tc.ts, tc.random, tc.token, got, want)
		}
		if got != strings.ToUpper(got) {
			t.Fatalf("signature %s is not upper-case; PHP strtoupper was not applied", got)
		}
		if len(got) != 32 {
			t.Fatalf("signature %s is not a 32-char md5 digest", got)
		}
	}
}

// TestStringSortOrderMatters pins the "sort as strings" behaviour: sorting
// numerically would produce a different digest for these inputs.
func TestStringSortOrderMatters(t *testing.T) {
	ts, random, token := int64(9), int64(10), "tok"
	// String order: "10","9","tok" -> 109tok
	stringSorted := strings.ToUpper(hexDigest("10" + "9" + "tok"))
	if got := Sign(ts, random, token); got != stringSorted {
		t.Fatalf("Sign used numeric ordering: got %s, want string-sorted %s", got, stringSorted)
	}
	numericSorted := strings.ToUpper(hexDigest("9" + "10" + "tok"))
	if Sign(ts, random, token) == numericSorted {
		t.Fatal("signature matched numeric ordering, PHP sorts as SORT_STRING")
	}
}

func hexDigest(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestParseRequest(t *testing.T) {
	get := func(values map[string]string) func(string) string {
		return func(k string) string { return values[k] }
	}
	if _, err := ParseRequest(get(map[string]string{})); err == nil {
		t.Fatal("empty request accepted")
	}
	if _, err := ParseRequest(get(map[string]string{"time": "abc", "signature": "x"})); err == nil {
		t.Fatal("non-numeric time accepted")
	}
	req, err := ParseRequest(get(map[string]string{"time": "1791082385", "random": "42", "signature": "ABC"}))
	if err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	if req.Timestamp != 1791082385 || req.Random != 42 || req.Signature != "ABC" {
		t.Fatalf("parsed request = %+v", req)
	}
}

func TestVerify(t *testing.T) {
	const token = "s3cr3t-token"
	now := time.Unix(1791082385, 0)

	good := Request{Timestamp: now.Unix(), Random: 99}
	good.Signature = Sign(good.Timestamp, good.Random, token)
	if err := good.Verify(token, DefaultTolerance, now); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	// Lower-case md5 is what most HTTP clients produce; PHP upper-cases it.
	lower := good
	lower.Signature = strings.ToLower(good.Signature)
	if err := lower.Verify(token, DefaultTolerance, now); err != nil {
		t.Fatalf("lower-case signature rejected: %v", err)
	}

	wrongToken := good
	wrongToken.Signature = Sign(good.Timestamp, good.Random, "other-token")
	if err := wrongToken.Verify(token, DefaultTolerance, now); err == nil {
		t.Fatal("signature for a different token was accepted")
	}

	expired := good
	expired.Timestamp = now.Add(-2 * DefaultTolerance).Unix()
	expired.Signature = Sign(expired.Timestamp, expired.Random, token)
	if err := expired.Verify(token, DefaultTolerance, now); err == nil {
		t.Fatal("stale timestamp was accepted (replay window not enforced)")
	}

	future := good
	future.Timestamp = now.Add(2 * DefaultTolerance).Unix()
	future.Signature = Sign(future.Timestamp, future.Random, token)
	if err := future.Verify(token, DefaultTolerance, now); err == nil {
		t.Fatal("far-future timestamp was accepted")
	}

	if err := good.Verify("", DefaultTolerance, now); err == nil {
		t.Fatal("empty token accepted")
	}
	if err := good.Verify(token, 0, now); err != nil {
		t.Fatalf("zero tolerance should fall back to the default window: %v", err)
	}
}
