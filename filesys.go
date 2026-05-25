package main

import "os"

func writeFileImpl(path string, data []byte) error {
	return os.WriteFile(path, data, 0644)
}

func deleteFileImpl(path string) error {
	return os.Remove(path)
}

func renameFileImpl(old, new string) error {
	return os.Rename(old, new)
}
