// Package apisign implements the 魔方 (ZJMF / 智简魔方) server-module request
// signature so ShitIDC can accept requests from a 魔方财务 "接口/服务器模块"
// and can also call one.
//
// The scheme is taken verbatim from the plaintext reference modules shipped
// with 魔方财务 3.7.6 (public/plugins/servers/bthosts/bthosts.php:14-23):
//
//	function bthosts_CreateSign($time, $random, $token)
//	{
//	    $data['time'] = $time;
//	    $data['random'] = $random;
//	    $data['token'] = $token;
//	    sort($data, SORT_STRING);
//	    $str = implode($data);
//	    $signature = md5($str);
//	    return strtoupper($signature);
//	}
//
// Two details matter for compatibility:
//
//   - sort($data, SORT_STRING) sorts the three values **as strings**, not the
//     keys, so the signature is md5 of time|random|token in lexicographic
//     string order.
//   - The result is upper-cased md5. Go's md5 hex output is lower-case, so it
//     must be upper-cased to match PHP.
//
// The shared token is never transmitted: it only participates in the digest.
// The request carries time, random and signature; the receiver looks the token
// up by the account it belongs to.
package apisign

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultTolerance bounds how far a request timestamp may drift from the
// server clock. The reference modules send time=time() (Unix seconds), so a
// tight window is enough and stops replay of a captured signature.
const DefaultTolerance = 5 * time.Minute

// Sign returns the upper-case md5 signature for (timestamp, random, token),
// byte-for-byte identical to the PHP modules.
func Sign(timestamp int64, random int64, token string) string {
	values := []string{strconv.FormatInt(timestamp, 10), strconv.FormatInt(random, 10), token}
	sort.Strings(values)
	sum := md5.Sum([]byte(strings.Join(values, "")))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// SignNow signs with the current clock and a caller-supplied nonce.
func SignNow(random int64, token string) string {
	return Sign(time.Now().Unix(), random, token)
}

// Request carries the authentication fields every 魔方 module sends.
type Request struct {
	Timestamp int64
	Random    int64
	Signature string
}

// ParseRequest reads time/random/signature out of the request values. 魔方
// modules post x-www-form-urlencoded bodies, but the same names work for query
// strings so a signed GET ping is accepted too.
func ParseRequest(get func(string) string) (Request, error) {
	rawTime := strings.TrimSpace(get("time"))
	rawRandom := strings.TrimSpace(get("random"))
	signature := strings.TrimSpace(get("signature"))
	if rawTime == "" || signature == "" {
		return Request{}, fmt.Errorf("missing time or signature")
	}
	ts, err := strconv.ParseInt(rawTime, 10, 64)
	if err != nil {
		return Request{}, fmt.Errorf("invalid time %q", rawTime)
	}
	// mt_rand() can exceed int32 on 64-bit PHP; ParseInt handles the range.
	var rnd int64
	if rawRandom != "" {
		rnd, err = strconv.ParseInt(rawRandom, 10, 64)
		if err != nil {
			return Request{}, fmt.Errorf("invalid random %q", rawRandom)
		}
	}
	return Request{Timestamp: ts, Random: rnd, Signature: signature}, nil
}

// Verify reports whether the request signature matches the token and whether
// its timestamp is inside the tolerance window. The returned error is safe to
// show to the caller.
func (r Request) Verify(token string, tolerance time.Duration, now time.Time) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("account has no API token configured")
	}
	if tolerance <= 0 {
		tolerance = DefaultTolerance
	}
	drift := now.Unix() - r.Timestamp
	if drift < 0 {
		drift = -drift
	}
	if time.Duration(drift)*time.Second > tolerance {
		return fmt.Errorf("request timestamp is outside the %s tolerance window", tolerance)
	}
	expected := Sign(r.Timestamp, r.Random, token)
	// Constant-time-ish comparison: both sides are fixed-width hex digests.
	if len(expected) != len(r.Signature) || !strings.EqualFold(expected, r.Signature) {
		return fmt.Errorf("invalid signature")
	}
	return nil
}
