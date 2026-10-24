package deedclient

import "os"

func readFile(path string) ([]byte, error) { return os.ReadFile(path) } //nolint:gosec // caller-chosen CA file
