package songya

import (
	"os"
	"path/filepath"
)

// Fixtures model externally created and interrupted records in the folder layout.
func fixtureWriteFile(name string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	return os.WriteFile(name, data, mode)
}
func fixtureRootWriteFile(root *os.Root, name string, data []byte, mode os.FileMode) error {
	if err := root.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	return root.WriteFile(name, data, mode)
}
func fixtureRootMkdir(root *os.Root, name string, mode os.FileMode) error {
	if err := root.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	return root.Mkdir(name, mode)
}
