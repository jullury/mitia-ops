package crypto

import "testing"

func TestRoundTrip(t *testing.T) {
	c, err := New("s3cr3t-master-key")
	if err != nil {
		t.Fatal(err)
	}
	enc, err := c.Encrypt("GARAGE_SECRET_ACCESS_KEY=hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if enc == "GARAGE_SECRET_ACCESS_KEY=hunter2" {
		t.Fatal("ciphertext should not equal plaintext")
	}
	dec, err := c.Decrypt(enc)
	if err != nil {
		t.Fatal(err)
	}
	if dec != "GARAGE_SECRET_ACCESS_KEY=hunter2" {
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
