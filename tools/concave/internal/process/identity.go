package process

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func Executable(dir, name string) (string, string, error) {
	if strings.ContainsAny(name, "/\\") && !filepath.IsAbs(name) {
		name = filepath.Join(dir, name)
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", "", err
	}
	p, err = filepath.EvalSymlinks(p)
	if err != nil {
		return "", "", err
	}
	f, err := os.Open(p)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, (256<<20)+1))
	if err != nil {
		return "", "", err
	}
	if n > 256<<20 {
		return "", "", fmt.Errorf("tool executable exceeds bound")
	}
	return p, hex.EncodeToString(h.Sum(nil)), nil
}
