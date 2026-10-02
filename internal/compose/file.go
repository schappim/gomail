package compose

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// LoadFile reads an attachment from disk. The content type comes from the
// extension, or from sniffing the content when the extension is unknown.
func LoadFile(path string) (File, error) {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return File{}, fmt.Errorf("attachment %s: file not found", path)
	case err != nil:
		return File{}, fmt.Errorf("attachment %s: %w", path, err)
	case info.IsDir():
		return File{}, fmt.Errorf("attachment %s: is a directory, not a file", path)
	case info.Size() > MaxAttachmentBytes:
		return File{}, fmt.Errorf("attachment %s: %s is over Gmail's 25 MB attachment limit; share it as a Google Drive link instead", path, humanSize(int(info.Size())))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return File{}, fmt.Errorf("attachment %s: %w", path, err)
	}
	name := filepath.Base(path)
	return File{
		Filename:    name,
		ContentType: DetectContentType(name, data),
		Data:        data,
	}, nil
}
