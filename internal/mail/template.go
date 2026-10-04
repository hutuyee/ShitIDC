package mail

import "strings"

// RenderTemplate fills {{placeholders}} in an admin-editable template.
// Unknown placeholders are dropped so templates stay safe to share.
func RenderTemplate(text string, vars map[string]string) string {
	out := text
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{{"+k+"}}", v)
	}
	// strip any remaining unknown placeholders
	for {
		start := strings.Index(out, "{{")
		if start < 0 {
			break
		}
		end := strings.Index(out[start:], "}}")
		if end < 0 {
			break
		}
		out = out[:start] + out[start+end+2:]
	}
	return out
}
