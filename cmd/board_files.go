package cmd

import (
	"fmt"
	"strings"

	"github.com/martins6/acolyte/internal/board"
)

// boardFiles resolves --file paths. Reference mode is the default; copy mode
// stores a durable snapshot under $ACOLYTE_HOME/board/files.
func boardFiles(raw []string, copyFiles bool) ([]board.File, error) {
	if len(raw) > board.MaxFilesPerEntry {
		return nil, fmt.Errorf("board: at most %d files per entry", board.MaxFilesPerEntry)
	}
	files := make([]board.File, 0, len(raw))
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		var (
			file board.File
			err  error
		)
		if copyFiles {
			file, err = board.CopyFile(item)
		} else {
			file, err = board.ReferenceFile(item)
		}
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, nil
}
