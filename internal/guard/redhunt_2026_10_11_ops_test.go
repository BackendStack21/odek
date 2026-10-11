package guard

import (
	"context"
	"testing"
)

// The local credential check only recognises algorithm-labelled PEM headers
// (RSA/DSA/EC/OPENSSH/PGP). The PKCS#8 header that `openssl genpkey`,
// Go's x509.MarshalPKCS8PrivateKey and GCP service-account JSON emit
// ("BEGIN PRIVATE KEY") and its encrypted variant are not matched, so a
// private key passes the memory/system-prompt guard scan as BENIGN.
func TestRED_LocalGuardMissesPKCS8PrivateKey(t *testing.T) {
	inputs := []string{
		"-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7\n-----END PRIVATE KEY-----",
		"-----BEGIN ENCRYPTED PRIVATE KEY-----\nMIIFHDBOBgkqhkiG9w0BBQ0wQTApBgkqhkiG9w0BBQwwHAQI\n-----END ENCRYPTED PRIVATE KEY-----",
		"-----BEGIN ED25519 PRIVATE KEY-----\nMC4CAQAwBQYDK2VwBCIEIA\n-----END ED25519 PRIVATE KEY-----",
		`{"type": "service_account", "private_key": "-----BEGIN PRIVATE KEY-----\nMIIEvQ\n-----END PRIVATE KEY-----\n"}`,
	}
	for _, in := range inputs {
		res, err := NewLocalGuard().Detect(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Injected {
			t.Errorf("private key accepted as %q by the local guard:\n%s", res.Label, in)
		}
		if err := ScanContent(context.Background(), "remember this key: "+in, nil, nil); err == nil {
			t.Errorf("ScanContent accepted a private key:\n%s", in)
		}
	}
	// Public material and prose about keys stay benign.
	for _, in := range []string{
		"-----BEGIN PUBLIC KEY-----\nMIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8A\n-----END PUBLIC KEY-----",
		"Generate a private key with openssl genpkey and keep it out of the repo.",
	} {
		if hasCredentials(in) {
			t.Errorf("non-secret flagged as a credential: %q", in)
		}
	}
}
