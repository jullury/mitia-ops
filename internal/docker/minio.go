package docker

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MinioClient describes how an ephemeral `mc` container connects to a service's
// MinIO server. HTTPAddr is the in-network URL (e.g. "http://minio:9000");
// Network is the compose network the running service is attached to, which the
// mc container joins so it can reach the server by name.
type MinioClient struct {
	HTTPAddr string
	User     string
	Password string
	Network  string
	Image    string // mc image; defaults to quay.io/minio/mc
}

// MinioImage is the default mc image used for bucket backup operations.
const MinioImage = "quay.io/minio/mc"

// mcImage returns the configured image or the default.
func (m MinioClient) mcImage() string {
	if m.Image != "" {
		return m.Image
	}
	return MinioImage
}

// run runs one mc command against the aliased server inside a disposable mc
// container joined to the service network, returning its combined output.
// extra are additional `docker run` options (e.g. "-v" mounts) prepended before
// the image.
func (m MinioClient) run(raw RawRunner, command string, extra ...string) (string, error) {
	script := fmt.Sprintf("mc alias set mitia %s %s %s && %s", m.HTTPAddr, m.User, m.Password, command)
	args := []string{"run", "--rm", "--network", m.Network}
	args = append(args, extra...)
	args = append(args, m.mcImage(), "sh", "-c", script)
	return raw.RunRaw(args...)
}

// minioLSItem is one line of `mc ls --json` output for a top-level bucket.
type minioLSItem struct {
	Status string `json:"status"`
	Key    string `json:"key"`
}

// ListMinioBuckets returns the names of all buckets on the server.
func ListMinioBuckets(raw RawRunner, mc MinioClient) ([]string, error) {
	out, err := mc.run(raw, "mc ls --json mitia")
	if err != nil {
		return nil, fmt.Errorf("list minio buckets: %w", err)
	}
	var buckets []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var item minioLSItem
		if derr := json.Unmarshal([]byte(line), &item); derr != nil {
			continue
		}
		if item.Status == "success" && item.Key != "" {
			buckets = append(buckets, item.Key)
		}
	}
	return buckets, nil
}

// MirrorMinioBucket copies a bucket's contents into hostDir (an absolute host
// path) via mc mirror.
func MirrorMinioBucket(raw RawRunner, mc MinioClient, bucket, hostDir string) error {
	if _, err := mc.run(raw, "mc mirror --overwrite mitia/"+bucket+" /dst", "-v", hostDir+":/dst"); err != nil {
		return fmt.Errorf("mirror minio bucket %s: %w", bucket, err)
	}
	return nil
}

// MakeMinioBucket creates a bucket if it does not already exist. An existing
// bucket is not an error (mc mb returns success on an already-present bucket).
func MakeMinioBucket(raw RawRunner, mc MinioClient, bucket string) error {
	if _, err := mc.run(raw, "mc mb --ignore-existing mitia/"+bucket); err != nil {
		return fmt.Errorf("make minio bucket %s: %w", bucket, err)
	}
	return nil
}

// RestoreMinioBucket mirrors the contents of hostDir (an absolute host path)
// back into a bucket via mc mirror.
func RestoreMinioBucket(raw RawRunner, mc MinioClient, bucket, hostDir string) error {
	if _, err := mc.run(raw, "mc mirror --overwrite /src mitia/"+bucket, "-v", hostDir+":/src"); err != nil {
		return fmt.Errorf("restore minio bucket %s: %w", bucket, err)
	}
	return nil
}
