package crypto

import "testing"

func TestRoundTrip(t *testing.T) {
	c, err := New("s3cr3t-master-key")
	if err != nil {
		t.Fatal(err)
	}
	enc, err := c.Encrypt("MINIO_ROOT_PASSWORD=hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if enc == "MINIO_ROOT_PASSWORD=hunter2" {
		t.Fatal("ciphertext should not equal plaintext")
	}
	dec, err := c.Decrypt(enc)
	if err != nil {
		t.Fatal(err)
	}
	if dec != "MINIO_ROOT_PASSWORD=hunter2" {
		t.Fatalf("round trip failed: got %q", dec)
	}
}

func TestWrongKeyFails(t *testing.T) {
	c1, _ := New("key-one")
	enc, _ := c1.Encrypt("secret")
	c2, _ := New("key-two")
	if _, err := c2.Decrypt(enc); err == nil {
		t.Fatal("expected decrypt with wrong key to fail")
	}
}

func TestEmptyMasterKeyRejected(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("expected empty master key to be rejected")
	}
}
