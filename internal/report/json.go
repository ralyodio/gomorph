package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func WriteJSON(path string, value any) error {
	var writer io.Writer = os.Stdout
	var file *os.File
	if path != "-" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create report directory: %w", err)
		}
		created, err := os.Create(path)
		if err != nil {
			return fmt.Errorf("create report: %w", err)
		}
		file = created
		writer = created
		defer file.Close()
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}
