package security

import (
	cryptorand "crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"strings"
)

// SVG math captcha (§9 验证码). Pure standard library: the challenge is a
// small arithmetic expression rendered as SVG with per-character jitter, the
// answer is verified by the API layer against a Redis-stored hash. The aim is
// to stop scripted bulk registration / brute force, not to be OCR-proof.

type Captcha struct {
	ID       string `json:"id"`
	SVG      string `json:"svg"`
	Answer   int    `json:"-"`
	Question string `json:"-"`
}

func randInt(n int) int {
	v, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}

// NewCaptcha generates a human-solvable arithmetic challenge and renders it
// as a self-contained SVG (no external assets, CSP friendly).
func NewCaptcha() (*Captcha, error) {
	idBytes := make([]byte, 16)
	if _, err := cryptorand.Read(idBytes); err != nil {
		return nil, err
	}
	a, b, op := 1+randInt(9), 1+randInt(9), randInt(2)
	var answer int
	var question string
	if op == 0 {
		answer, question = a+b, fmt.Sprintf("%d + %d", a, b)
	} else {
		if b > a {
			a, b = b, a
		}
		answer, question = a-b, fmt.Sprintf("%d - %d", a, b)
	}
	svg := renderCaptchaSVG(question)
	return &Captcha{ID: base64.RawURLEncoding.EncodeToString(idBytes), SVG: svg, Answer: answer, Question: question}, nil
}

func renderCaptchaSVG(question string) string {
	var sb strings.Builder
	sb.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" width="160" height="48" viewBox="0 0 160 48" role="img" aria-label="验证码">`)
	sb.WriteString(`<rect width="160" height="48" rx="8" fill="var(--panel, #f6f7fb)"/>`)
	// scattered interference lines
	for i := 0; i < 4; i++ {
		x1, y1, x2, y2 := randInt(160), randInt(48), randInt(160), randInt(48)
		sb.WriteString(fmt.Sprintf(`<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="var(--muted, #9aa3b5)" stroke-opacity="0.35" stroke-width="1"/>`, x1, y1, x2, y2))
	}
	x := 14
	for _, ch := range question {
		if ch == ' ' {
			x += 6
			continue
		}
		dy := randInt(10) - 5
		rot := randInt(30) - 15
		sb.WriteString(fmt.Sprintf(
			`<text x="%d" y="%d" transform="rotate(%d %d %d)" font-family="Georgia, serif" font-size="26" font-weight="700" fill="var(--text, #1c2333)">%s</text>`,
			x, 32+dy, rot, x, 32+dy, templateEscape(string(ch))))
		x += 18
	}
	sb.WriteString(`</svg>`)
	return sb.String()
}

func templateEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
