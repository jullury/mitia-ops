package docker

import (
	"strings"
	"testing"
)

func TestListS3BucketsParsesJSON(t *testing.T) {
	raw := &fakeRaw{
		out: `{"status":"success","type":"folder","size":0,"key":"photos"}
{"status":"success","type":"folder","size":0,"key":"docs"}
`,
	}
	mc := S3Client{HTTPAddr: "http://garage:3900", User: "u", Password: "p", Network: "proj_default"}
	got, err := ListS3Buckets(raw, mc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "photos" || got[1] != "docs" {
		t.Fatalf("unexpected buckets %v", got)
	}
}

func TestListS3BucketsRequiresAliasAndNetwork(t *testing.T) {
	raw := &fakeRaw{}
	mc := S3Client{HTTPAddr: "http://garage:3900", User: "u", Password: "p", Network: "proj_default"}
	if _, err := ListS3Buckets(raw, mc); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(raw.args, " ")
	if !strings.Contains(joined, "--network proj_default") {
		t.Fatalf("should join the compose network: %v", raw.args)
	}
	if !strings.Contains(joined, "mc alias set") {
		t.Fatalf("should set the mc alias: %v", raw.args)
	}
}

func TestMirrorS3BucketMountsHostDir(t *testing.T) {
	raw := &fakeRaw{}
	mc := S3Client{HTTPAddr: "http://garage:3900", User: "u", Password: "p", Network: "proj_default"}
	if err := MirrorS3Bucket(raw, mc, "photos", "/staging/photos"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(raw.args, " ")
	if !strings.Contains(joined, "-v /staging/photos:/dst") {
		t.Fatalf("should mount host dir for the bucket: %v", raw.args)
	}
	if !strings.Contains(joined, "mc mirror") {
		t.Fatalf("should run mc mirror: %v", raw.args)
	}
	if !strings.Contains(joined, "mitia/photos") {
		t.Fatalf("should reference the aliased bucket: %v", raw.args)
	}
}

func TestMakeS3BucketRunsMcMB(t *testing.T) {
	raw := &fakeRaw{}
	mc := S3Client{HTTPAddr: "http://garage:3900", User: "u", Password: "p", Network: "proj_default"}
	if err := MakeS3Bucket(raw, mc, "photos"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(raw.args, " ")
	if !strings.Contains(joined, "mc mb") {
		t.Fatalf("should run mc mb: %v", raw.args)
	}
}

func TestRestoreS3BucketMirrorsLocalToAlias(t *testing.T) {
	raw := &fakeRaw{}
	mc := S3Client{HTTPAddr: "http://garage:3900", User: "u", Password: "p", Network: "proj_default"}
	if err := RestoreS3Bucket(raw, mc, "photos", "/staging/photos"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(raw.args, " ")
	if !strings.Contains(joined, "-v /staging/photos:/src") {
		t.Fatalf("should mount host dir as source: %v", raw.args)
	}
	if !strings.Contains(joined, "mc mirror") {
		t.Fatalf("should run mc mirror to restore: %v", raw.args)
	}
}

func TestMCClientOverridesEntrypointForShellScript(t *testing.T) {
	// The quay.io/minio/mc image sets its entrypoint to `mc`, so running
	// `sh -c "<script>"` requires overriding the entrypoint; otherwise the
	// script (and even `sh`) is parsed as an mc subcommand and every operation
	// fails. Guard against regression.
	raw := &fakeRaw{}
	mc := S3Client{HTTPAddr: "http://garage:3900", User: "u", Password: "p", Network: "proj_default"}
	if _, err := ListS3Buckets(raw, mc); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(raw.args, " ")
	if !strings.Contains(joined, "--entrypoint /bin/sh") {
		t.Fatalf("mc container must override the entrypoint to run a shell script: %v", raw.args)
	}
	if !strings.Contains(joined, " -c ") {
		t.Fatalf("mc container must pass the script via -c: %v", raw.args)
	}
}
