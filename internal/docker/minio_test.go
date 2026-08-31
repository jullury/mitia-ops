package docker

import (
	"strings"
	"testing"
)

func TestListMinioBucketsParsesJSON(t *testing.T) {
	raw := &fakeRaw{
		out: `{"status":"success","type":"folder","size":0,"key":"photos"}
{"status":"success","type":"folder","size":0,"key":"docs"}
`,
	}
	mc := MinioClient{HTTPAddr: "http://minio:9000", User: "u", Password: "p", Network: "proj_default"}
	got, err := ListMinioBuckets(raw, mc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "photos" || got[1] != "docs" {
		t.Fatalf("unexpected buckets %v", got)
	}
}

func TestListMinioBucketsRequiresAliasAndNetwork(t *testing.T) {
	raw := &fakeRaw{}
	mc := MinioClient{HTTPAddr: "http://minio:9000", User: "u", Password: "p", Network: "proj_default"}
	if _, err := ListMinioBuckets(raw, mc); err != nil {
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

func TestMirrorMinioBucketMountsHostDir(t *testing.T) {
	raw := &fakeRaw{}
	mc := MinioClient{HTTPAddr: "http://minio:9000", User: "u", Password: "p", Network: "proj_default"}
	if err := MirrorMinioBucket(raw, mc, "photos", "/staging/photos"); err != nil {
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

func TestMakeMinioBucketRunsMcMB(t *testing.T) {
	raw := &fakeRaw{}
	mc := MinioClient{HTTPAddr: "http://minio:9000", User: "u", Password: "p", Network: "proj_default"}
	if err := MakeMinioBucket(raw, mc, "photos"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(raw.args, " ")
	if !strings.Contains(joined, "mc mb") {
		t.Fatalf("should run mc mb: %v", raw.args)
	}
}

func TestRestoreMinioBucketMirrorsLocalToAlias(t *testing.T) {
	raw := &fakeRaw{}
	mc := MinioClient{HTTPAddr: "http://minio:9000", User: "u", Password: "p", Network: "proj_default"}
	if err := RestoreMinioBucket(raw, mc, "photos", "/staging/photos"); err != nil {
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
