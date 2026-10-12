package process

import (
	"bytes"
	"os/exec"
	"reflect"
	"testing"
)

func TestShellCommandPreservesArguments(t *testing.T) {
	args := []string{"", "plain", "space space", "single'quote", "double\"quote", "$HOME", "$(printf expanded)", "`printf expanded`", "line\nbreak", "#comment", "a;b", "café", "\\", "*.go"}
	command := exec.Command("sh", "-c", "printf '%s\\0' "+ShellCommand(args))
	data, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	var actual []string
	for _, arg := range bytes.Split(bytes.TrimSuffix(data, []byte{0}), []byte{0}) {
		actual = append(actual, string(arg))
	}
	if !reflect.DeepEqual(actual, args) {
		t.Fatalf("got %#v, want %#v", actual, args)
	}
}
