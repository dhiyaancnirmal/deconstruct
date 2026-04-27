package blobstore

import "testing"

func TestBlobStoreRoundTrip(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"message":"hello"}`)
	hash, err := store.Put(original)
	if err != nil {
		t.Fatal(err)
	}
	if hash == "" {
		t.Fatal("expected hash")
	}
	restored, err := store.Get(hash)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(original) {
		t.Fatalf("unexpected blob: %q", restored)
	}
}
