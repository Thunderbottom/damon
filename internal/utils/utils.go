package utils

import (
	"fmt"
	"os"
)

// IsExists checks whether a path exists
func IsExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("error checking path: %w", err)
	}
	return true, nil
}

// ReadFile reads a file and returns its contents
func ReadFile(path string) ([]byte, error) {
	exists, err := IsExists(path)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("file does not exist: %s", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("error reading file: %w", err)
	}
	return data, nil
}
