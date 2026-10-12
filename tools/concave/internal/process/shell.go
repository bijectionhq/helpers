package process

import "strings"

// ShellCommand renders argv as a POSIX shell command for a human to copy.
// Pass argv directly when possible; land.py check commands require shell text.
func ShellCommand(args []string) string {
	words := make([]string, len(args))
	for index, arg := range args {
		safe := arg != ""
		for _, char := range arg {
			if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("_@%+=:,./-", char)) {
				safe = false
				break
			}
		}
		if safe {
			words[index] = arg
		} else {
			words[index] = "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
		}
	}
	return strings.Join(words, " ")
}
