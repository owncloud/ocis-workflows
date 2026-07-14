package secretbox

import "testing"

func TestSealOpenRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	box, err := New(key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sealed, err := box.Seal("s3cr3t-app-password")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if sealed == "s3cr3t-app-password" {
		t.Fatal("Seal returned plaintext unchanged")
	}

	opened, err := box.Open(sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened != "s3cr3t-app-password" {
		t.Fatalf("Open() = %q, want original plaintext", opened)
	}
}

func TestNewRejectsWrongKeySize(t *testing.T) {
	if _, err := New([]byte("too-short")); err == nil {
		t.Fatal("expected error for a non-32-byte key")
	}
}

func TestOpenRejectsTamperedCiphertext(t *testing.T) {
	key := make([]byte, 32)
	box, _ := New(key)
	sealed, _ := box.Seal("hello")

	tampered := sealed[:len(sealed)-4] + "abcd"
	if _, err := box.Open(tampered); err == nil {
		t.Fatal("expected error opening tampered ciphertext")
	}
}
