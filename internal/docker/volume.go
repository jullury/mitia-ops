package docker

import (
	"fmt"
	"os"
	"path/filepath"
)

// VolumeName returns the project-scoped Docker volume name Composer creates
// for the named volume `name` when running a compose file located in dir.
// Docker prefixes named volumes with the compose project name, which defaults
// to the base name of the project directory.
func VolumeName(dir, name string) string {
	return filepath.Base(dir) + "_" + name
}

// VolumeExists reports whether a Docker volume with the given name exists.
func VolumeExists(raw RawRunner, volumeName string) (bool, error) {
	if _, err := raw.RunRaw("volume", "inspect", volumeName); err != nil {
		// docker volume inspect fails when the volume does not exist.
		return false, nil
	}
	return true, nil
}

// CreateVolume creates a plain local-driver volume. Docker's local driver
// cannot enforce a size on persistent storage (the size mount option is only
// valid for tmpfs), so a size is deliberately not passed here: the configured
// MINIO_VOLUME_SIZE is advisory and is only used for the free-space preflight.
func CreateVolume(raw RawRunner, volumeName string) error {
	args := []string{"volume", "create", "--driver", "local", volumeName}
	if _, err := raw.RunRaw(args...); err != nil {
		return fmt.Errorf("create volume %s: %w", volumeName, err)
	}
	return nil
}

// EnsureVolume creates volumeName if it does not already exist.
func EnsureVolume(raw RawRunner, volumeName string) error {
	ok, err := VolumeExists(raw, volumeName)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	return CreateVolume(raw, volumeName)
}

// RelocateVolume moves a Docker volume's data to a new name without data loss.
// `docker volume rename` is not available on every Docker install, so the move
// is done as a copy: the target is recreated empty, the source's contents are
// archived and restored into it, then the source is removed. Idempotent: when
// the source is already gone the volume is treated as moved, and a partial or
// stale target is discarded because the still-present source holds the
// authoritative data. Callers must take the containing project down first so no
// container holds either volume open.
func RelocateVolume(raw RawRunner, from, to string) error {
	fromExists, err := VolumeExists(raw, from)
	if err != nil {
		return err
	}
	if !fromExists {
		return nil
	}
	if ok, _ := VolumeExists(raw, to); ok {
		if err := RemoveVolume(raw, to); err != nil {
			return fmt.Errorf("drop stale target %s: %w", to, err)
		}
	}
	if err := CreateVolume(raw, to); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "volume-migrate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := BackupVolume(raw, from, tmp, VolumeImage); err != nil {
		return fmt.Errorf("backup %s: %w", from, err)
	}
	if err := RestoreVolume(raw, to, tmp, VolumeImage); err != nil {
		return fmt.Errorf("restore into %s: %w", to, err)
	}
	if err := RemoveVolume(raw, from); err != nil {
		return fmt.Errorf("remove %s: %w", from, err)
	}
	return nil
}

// BackupVolume archives the current contents of volume into backupDir as
// volume.tgz by running a throwaway image that mounts both paths.
func BackupVolume(raw RawRunner, volumeName, backupDir, image string) error {
	args := []string{"run", "--rm",
		"-v", volumeName + ":/from:ro",
		"-v", backupDir + ":/backup",
		image,
		"tar", "-czf", "/backup/volume.tgz", "-C", "/from", ".",
	}
	if _, err := raw.RunRaw(args...); err != nil {
		return fmt.Errorf("backup volume %s: %w", volumeName, err)
	}
	return nil
}

// RestoreVolume unpacks the volume.tgz previously written by BackupVolume into
// volume.
func RestoreVolume(raw RawRunner, volumeName, backupDir, image string) error {
	args := []string{"run", "--rm",
		"-v", volumeName + ":/to",
		"-v", backupDir + ":/backup",
		image,
		"sh", "-c", "tar -xzf /backup/volume.tgz -C /to",
	}
	if _, err := raw.RunRaw(args...); err != nil {
		return fmt.Errorf("restore volume %s: %w", volumeName, err)
	}
	return nil
}

// RemoveVolume deletes a Docker volume by name.
func RemoveVolume(raw RawRunner, volumeName string) error {
	if _, err := raw.RunRaw("volume", "rm", volumeName); err != nil {
		return fmt.Errorf("remove volume %s: %w", volumeName, err)
	}
	return nil
}

// VolumeImage is the lightweight helper image used to move volume data.
const VolumeImage = "alpine:3.20"
