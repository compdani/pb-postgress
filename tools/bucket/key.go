package bucket

import (
	"errors"
	"path/filepath"
	"strings"
)

var (
	// ErrInvalidKey is returned when an object path escapes the storage root
	// or contains a segment that is not a single file name.
	ErrInvalidKey = errors.New("invalid object key")

	// ErrNotFound is returned when the object file does not exist.
	ErrNotFound = errors.New("object not found")

	// ErrTooLarge is returned when an upload exceeds the configured limit.
	ErrTooLarge = errors.New("object exceeds size limit")
)

// validSegment reports whether s can be one path segment of a stored object.
func validSegment(s string) error {
	if s == "" || len(s) > 255 {
		return ErrInvalidKey
	}
	if s == "." || s == ".." || strings.HasPrefix(s, ".tmp-") {
		return ErrInvalidKey
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			return ErrInvalidKey
		}
	}
	return nil
}

func objectKey(collection, recordID, filename string) string {
	return collection + "/" + recordID + "/" + filename
}

// cleanPrefix validates a list prefix. An empty prefix matches every object.
func cleanPrefix(prefix string) (string, error) {
	if prefix == "" {
		return "", nil
	}
	if strings.ContainsRune(prefix, 0) || strings.Contains(prefix, "\\") {
		return "", ErrInvalidKey
	}
	prefix = strings.TrimPrefix(prefix, "/")
	if strings.Contains(prefix, "//") {
		return "", ErrInvalidKey
	}
	trimmed := strings.Trim(prefix, "/")
	if trimmed == "" {
		return "", ErrInvalidKey
	}
	for _, part := range strings.Split(trimmed, "/") {
		if err := validSegment(part); err != nil {
			return "", err
		}
	}
	return prefix, nil
}

// withinRoot reports whether full is lexically inside root.
func withinRoot(root, full string) error {
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return ErrInvalidKey
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ErrInvalidKey
	}
	return nil
}

// maxNameDepth bounds the segments below collection/recordId. PocketBase
// stores thumbnails one level down (thumbs_<file>/<size>_<file>).
const maxNameDepth = 4

// splitName validates a filename that may contain nested "/" segments.
func splitName(filename string) ([]string, error) {
	parts := strings.Split(filename, "/")
	if len(parts) > maxNameDepth {
		return nil, ErrInvalidKey
	}
	for _, part := range parts {
		if err := validSegment(part); err != nil {
			return nil, err
		}
	}
	return parts, nil
}
