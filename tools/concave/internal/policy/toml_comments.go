package policy

import "strings"

// tomlComments extracts comment tokens, not TOML values. Basic/quoted keys and
// all four string forms are opaque. Incomplete strings stay opaque to EOF;
// source validation remains the manifest owner's responsibility.
// String delimiters: https://toml.io/en/v1.0.0#string.
func tomlComments(text string) map[int]string {
	out := map[int]string{}
	line, lineStart := 0, 0
	advance := func(i int) int {
		if text[i] == '\n' {
			line++
			lineStart = i + 1
		}
		return i + 1
	}
	for i := 0; i < len(text); {
		if text[i] == '#' {
			end := strings.IndexByte(text[i:], '\n')
			if end < 0 {
				end = len(text) - i
			}
			end += i
			if strings.Trim(text[lineStart:i], " \t") == "" {
				out[line] = strings.TrimSpace(text[i+1 : end])
			}
			i = end
			continue
		}
		quote := text[i]
		if quote != '\'' && quote != '"' {
			i = advance(i)
			continue
		}
		width := 1
		if i+2 < len(text) && text[i+1] == quote && text[i+2] == quote {
			width = 3
		}
		i += width
		for i < len(text) {
			if quote == '"' && text[i] == '\\' {
				i++
				if i < len(text) {
					i = advance(i)
				}
				continue
			}
			if text[i] == quote {
				if width == 1 {
					i++
					break
				}
				end := i
				for end < len(text) && text[end] == quote {
					end++
				}
				// Four or five closing quotes include one or two literal
				// quotes. Consume them with the closing delimiter.
				closed := end-i >= width
				i = end
				if closed {
					break
				}
				continue
			}
			i = advance(i)
		}
	}
	return out
}
