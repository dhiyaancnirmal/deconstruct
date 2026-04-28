package mobile

import "testing"

func TestBuildMobileSetup(t *testing.T) {
	setup, err := Build("127.0.0.1:18080", "/tmp/deconstruct-ca.pem")
	if err != nil {
		t.Fatal(err)
	}
	if setup.ProxyPort != 18080 || setup.ProxyURL == "" || len(setup.QRPayloads) == 0 {
		t.Fatalf("unexpected setup: %#v", setup)
	}
	if len(setup.Instructions["ios"]) == 0 || len(setup.Instructions["android"]) == 0 {
		t.Fatalf("expected platform instructions: %#v", setup.Instructions)
	}
}
