package docker

import (
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
)

// ParseSize converts a Docker-style size string ("100G", "512M", "1T", "1024")
// into bytes. A bare number is treated as MiB (matching Docker's convention).
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "G"):
		mult = 1 << 30
		s = strings.TrimSuffix(s, "G")
	case strings.HasSuffix(s, "M"):
		mult = 1 << 20
		s = strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "K"):
		mult = 1 << 10
		s = strings.TrimSuffix(s, "K")
	case strings.HasSuffix(s, "T"):
		mult = 1 << 40
		s = strings.TrimSuffix(s, "T")
	case strings.HasSuffix(s, "B"):
		mult = 1
		s = strings.TrimSuffix(s, "B")
	default:
		// bare number -> MiB, like Docker
		mult = 1 << 20
	}
	s = strings.TrimSpace(s)
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return n * mult, nil
}

// BytesAvailable returns the free disk space (bytes) on the filesystem
// containing path. It is used as a pre-flight safety check before a resize.
func BytesAvailable(path string) (int64, error) {
	// Statfs needs an existing path. The deploy directory may not exist yet
	// (e.g. on the first save before the compose file is written), so walk up to
	// the nearest existing ancestor — the filesystem is what matters, not the
	// specific leaf directory.
	for {
		var st syscall.Statfs_t
		err := syscall.Statfs(path, &st)
		if err == nil {
			// Bsize is int64 on Linux; guard for platforms where it is int32.
			bsize := int64(st.Bsize)
			if bsize == 0 {
				bsize = 4096
			}
			return int64(st.Bavail) * bsize, nil
		}
		parent := filepath.Dir(path)
		if parent == path {
			return 0, err
		}
		path = parent
	}
}
