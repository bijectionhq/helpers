package repository

import (
	"fmt"
	"os"
	"strings"
)

// maxTreeBytes bounds one ReadTree.
const maxTreeBytes = MaxOutput / 2

// ReadTree reads every file under one directory prefix: a revision view's
// blobs, or the working files there, walked once without Git and without
// following symlinks. A missing directory is empty. Each file must be a
// regular, non-executable file of at most MaxBytes; anything else under the
// prefix is an error naming it.
func (repo *Repo) ReadTree(prefix string) (map[string][]byte, error) {
	if !ValidPath(prefix) || !strings.HasSuffix(prefix, "/") {
		return nil, fmt.Errorf("unsafe directory %q", prefix)
	}
	files := map[string][]byte{}
	total := 0
	add := func(path string, data []byte) error {
		total += len(data)
		if total > maxTreeBytes {
			return fmt.Errorf("%s exceeds %d bytes", prefix, maxTreeBytes)
		}
		files[path] = data
		return nil
	}
	if repo.Revision != "" {
		for path, entry := range repo.sourceTree {
			if !strings.HasPrefix(path, prefix) {
				continue
			}
			var data []byte
			var err error
			if entry.Mode == "100644" {
				data, err = repo.Blob(entry.OID)
			}
			if err != nil {
				return nil, err
			}
			if data == nil {
				return nil, fmt.Errorf("%s must be a regular non-executable file of at most %d bytes", path, MaxBytes)
			}
			if err := add(path, data); err != nil {
				return nil, err
			}
		}
		return files, nil
	}
	root, err := os.OpenRoot(repo.Root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	dir := strings.TrimSuffix(prefix, "/")
	for _, part := range strings.Split(dir, "/") {
		info, err := root.Lstat(part)
		if os.IsNotExist(err) {
			return files, nil
		}
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%s must be a directory", dir)
		}
		next, err := root.OpenRoot(part)
		if err != nil {
			return nil, err
		}
		root.Close()
		root = next
	}
	var walk func(*os.Root, string) error
	walk = func(directory *os.Root, name string) error {
		handle, err := directory.Open(".")
		if err != nil {
			return err
		}
		entries, err := handle.ReadDir(-1)
		handle.Close()
		if err != nil {
			return err
		}
		for _, entry := range entries {
			path := name + "/" + entry.Name()
			switch {
			case entry.IsDir():
				child, err := directory.OpenRoot(entry.Name())
				if err != nil {
					return err
				}
				err = walk(child, path)
				child.Close()
				if err != nil {
					return err
				}
			case entry.Type().IsRegular():
				info, err := entry.Info()
				if err != nil {
					return err
				}
				if info.Mode()&0o111 != 0 || info.Size() > MaxBytes {
					return fmt.Errorf("%s must be a regular non-executable file of at most %d bytes", path, MaxBytes)
				}
				data, err := directory.ReadFile(entry.Name())
				if err != nil {
					return err
				}
				if len(data) > MaxBytes {
					return &Stale{Message: "input changed while reading: " + path}
				}
				if err := add(path, data); err != nil {
					return err
				}
			default:
				return fmt.Errorf("%s must be a regular file or directory", path)
			}
		}
		return nil
	}
	if err := walk(root, dir); err != nil {
		return nil, err
	}
	return files, nil
}
