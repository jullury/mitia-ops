package docker

import (
	"encoding/json"
	"fmt"
	"strings"
)

// S3Client describes how an ephemeral `mc` container connects to a service's
// S3-compatible server (e.g. Garage). HTTPAddr is the in-network URL
// (e.g. "http://garage:3900"); Network is the compose network the running
// service is attached to, which the mc container joins so it can reach the
// server by name.
type S3Client struct {
	HTTPAddr string
	User     string
	Password string
	Network  string
	Image    string // mc image; defaults to quay.io/minio/mc
}

// S3Image is the default mc image used for bucket backup operations.
const S3Image = "quay.io/minio/mc"

// GarageCLI runs a `garage` CLI subcommand inside a running garage container for
// an app-managed service. container is the compose service container name (e.g.
// "<id>-garage-1"); args are the `/garage` CLI arguments. Used for first-boot
// cluster setup (layout assign/apply, key import) that must run against the
// live daemon once it is reachable.
func GarageCLI(raw RawRunner, container string, args ...string) (string, error) {
	return raw.RunRaw(append([]string{"exec", container, "/garage"}, args...)...)
}

// mcImage returns the configured image or the default.
func (m S3Client) mcImage() string {
	if m.Image != "" {
		return m.Image
	}
	return S3Image
}

// run runs one mc command against the aliased server inside a disposable mc
// container joined to the service network, returning its combined output.
// extra are additional `docker run` options (e.g. "-v" mounts) prepended before
// the image. The mc image's entrypoint is `mc`, so we override it with a shell
// in order to chain `mc alias set` ahead of the requested command.
func (m S3Client) run(raw RawRunner, command string, extra ...string) (string, error) {
	script := fmt.Sprintf("mc alias set mitia %s %s %s && %s", m.HTTPAddr, m.User, m.Password, command)
	args := []string{"run", "--rm", "--network", m.Network}
	args = append(args, extra...)
	args = append(args, "--entrypoint", "/bin/sh", m.mcImage(), "-c", script)
	return raw.RunRaw(args...)
}

// s3LSItem is one line of `mc ls --json` output for a top-level bucket.
type s3LSItem struct {
	Status string `json:"status"`
	Key    string `json:"key"`
}

// ListS3Buckets returns the names of all buckets on the server.
func ListS3Buckets(raw RawRunner, mc S3Client) ([]string, error) {
	out, err := mc.run(raw, "mc ls --json mitia")
	if err != nil {
		return nil, fmt.Errorf("list s3 buckets: %w", err)
	}
	var buckets []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var item s3LSItem
		if derr := json.Unmarshal([]byte(line), &item); derr != nil {
			continue
		}
		if item.Status == "success" && item.Key != "" {
			buckets = append(buckets, item.Key)
		}
	}
	return buckets, nil
}

// MirrorS3Bucket copies a bucket's contents into hostDir (an absolute host
// path) via mc mirror.
func MirrorS3Bucket(raw RawRunner, mc S3Client, bucket, hostDir string) error {
	if _, err := mc.run(raw, "mc mirror --overwrite mitia/"+bucket+" /dst", "-v", hostDir+":/dst"); err != nil {
		return fmt.Errorf("mirror s3 bucket %s: %w", bucket, err)
	}
	return nil
}

// MakeS3Bucket creates a bucket if it does not already exist. An existing
// bucket is not an error (mc mb returns success on an already-present bucket).
func MakeS3Bucket(raw RawRunner, mc S3Client, bucket string) error {
	if _, err := mc.run(raw, "mc mb --ignore-existing mitia/"+bucket); err != nil {
		return fmt.Errorf("make s3 bucket %s: %w", bucket, err)
	}
	return nil
}

// RestoreS3Bucket mirrors the contents of hostDir (an absolute host path) back
// into a bucket via mc mirror.
func RestoreS3Bucket(raw RawRunner, mc S3Client, bucket, hostDir string) error {
	if _, err := mc.run(raw, "mc mirror --overwrite /src mitia/"+bucket, "-v", hostDir+":/src"); err != nil {
		return fmt.Errorf("restore s3 bucket %s: %w", bucket, err)
	}
	return nil
}
