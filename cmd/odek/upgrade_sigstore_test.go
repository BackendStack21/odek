package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// testSigstore is an in-test Fulcio-like CA plus a Rekor-like log key.
type testSigstore struct {
	rootKey, interKey *ecdsa.PrivateKey
	root, inter       *x509.Certificate
	rekorKey          *ecdsa.PrivateKey
	logID             []byte
	trust             *sigstoreTrust
}

func newTestSigstore(t *testing.T) *testSigstore {
	t.Helper()
	ts := &testSigstore{}
	ts.rootKey = mustECKey(t, elliptic.P384())
	ts.interKey = mustECKey(t, elliptic.P384())
	ts.rekorKey = mustECKey(t, elliptic.P256())
	notBefore := time.Now().Add(-24 * time.Hour)
	notAfter := time.Now().Add(365 * 24 * time.Hour)

	ts.root = mustCert(t, &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{Organization: []string{"test"}, CommonName: "test-root"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}, nil, &ts.rootKey.PublicKey, ts.rootKey)
	ts.inter = mustCert(t, &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{Organization: []string{"test"}, CommonName: "test-intermediate"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}, ts.root, &ts.interKey.PublicKey, ts.rootKey)

	der, err := x509.MarshalPKIXPublicKey(&ts.rekorKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	id := sha256.Sum256(der)
	ts.logID = id[:]
	trust, err := newSigstoreTrust(
		[]string{string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.root.Raw}))},
		[]string{string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.inter.Raw}))},
		[]string{string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))},
	)
	if err != nil {
		t.Fatal(err)
	}
	ts.trust = trust
	return ts
}

// install points upgradeTrust at this test CA until the test ends.
func (ts *testSigstore) install(t *testing.T) {
	t.Helper()
	prev := upgradeTrust
	upgradeTrust = ts.trust
	t.Cleanup(func() { upgradeTrust = prev })
}

func mustECKey(t *testing.T, c elliptic.Curve) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(c, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func mustCert(t *testing.T, tmpl, parent *x509.Certificate, pub *ecdsa.PublicKey, signer *ecdsa.PrivateKey) *x509.Certificate {
	t.Helper()
	if parent == nil {
		parent = tmpl
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, signer)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func utf8Ext(t *testing.T, s string) []byte {
	t.Helper()
	b, err := asn1.MarshalWithParams(s, "utf8")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// testBundleOpts selects how a test bundle deviates from a valid one.
type testBundleOpts struct {
	tag            string
	san            string // overrides the workflow identity
	issuer         string // overrides the OIDC issuer (both extensions)
	legacyIssuer   bool   // only the legacy raw issuer extension
	repo           string // overrides the source-repository extension
	untrustedCA    bool   // leaf issued by a CA outside the trust
	leafExpired    bool   // leaf validity ends before the integration time
	badSET         bool   // corrupt the signed entry timestamp
	noSET          bool
	noProof        bool
	badProof       bool   // wrong audit path
	badCheckpoint  bool   // checkpoint signed by another key
	signOther      []byte // message signature over different bytes
	bodyDigest     string // overrides the digest recorded in the log body
	otherLogKey    bool   // log ID not in the trust
	kind           string // overrides kindVersion.kind
	mediaType      string
	noMessageSig   bool
	noCert         bool
	integratedTime int64 // overrides the integration time
}

func (ts *testSigstore) bundle(t *testing.T, artifact []byte, o testBundleOpts) []byte {
	t.Helper()
	san := o.san
	if san == "" {
		san = upgradeWorkflow + "@refs/tags/" + o.tag
	}
	sanURL, err := url.Parse(san)
	if err != nil {
		t.Fatal(err)
	}
	issuer := o.issuer
	if issuer == "" {
		issuer = upgradeOIDCIssuer
	}
	repo := o.repo
	if repo == "" {
		repo = upgradeSourceRepo
	}
	integrated := time.Now().Add(-time.Minute)
	if o.integratedTime != 0 {
		integrated = time.Unix(o.integratedTime, 0)
	}
	leafNotBefore := integrated.Add(-time.Minute)
	leafNotAfter := integrated.Add(9 * time.Minute)
	if o.leafExpired {
		leafNotAfter = integrated.Add(-time.Second)
	}

	exts := []pkix.Extension{{Id: oidFulcioIssuerLegacy, Value: []byte(issuer)}}
	if !o.legacyIssuer {
		exts = append(exts,
			pkix.Extension{Id: oidFulcioIssuer, Value: utf8Ext(t, issuer)},
			pkix.Extension{Id: oidFulcioSourceRepo, Value: utf8Ext(t, repo)})
	}
	leafKey := mustECKey(t, elliptic.P256())
	parent, parentKey := ts.inter, ts.interKey
	if o.untrustedCA {
		other := newTestSigstore(t)
		parent, parentKey = other.inter, other.interKey
	}
	leaf := mustCert(t, &x509.Certificate{
		SerialNumber:    big.NewInt(time.Now().UnixNano()),
		NotBefore:       leafNotBefore,
		NotAfter:        leafNotAfter,
		KeyUsage:        x509.KeyUsageDigitalSignature,
		ExtKeyUsage:     []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		URIs:            []*url.URL{sanURL},
		ExtraExtensions: exts,
	}, parent, &leafKey.PublicKey, parentKey)

	digest := sha256.Sum256(artifact)
	signed := digest
	if o.signOther != nil {
		signed = sha256.Sum256(o.signOther)
	}
	sig, err := ecdsa.SignASN1(rand.Reader, leafKey, signed[:])
	if err != nil {
		t.Fatal(err)
	}

	bodyDigest := hex.EncodeToString(digest[:])
	if o.bodyDigest != "" {
		bodyDigest = o.bodyDigest
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})
	body := fmt.Sprintf(`{"apiVersion":"0.0.1","kind":"hashedrekord","spec":{"data":{"hash":{"algorithm":"sha256","value":%q}},"signature":{"content":%q,"publicKey":{"content":%q}}}}`,
		bodyDigest, base64.StdEncoding.EncodeToString(sig), base64.StdEncoding.EncodeToString(certPEM))

	logKey, logID := ts.rekorKey, ts.logID
	if o.otherLogKey {
		logKey = mustECKey(t, elliptic.P256())
		der, _ := x509.MarshalPKIXPublicKey(&logKey.PublicKey)
		id := sha256.Sum256(der)
		logID = id[:]
	}
	const logIndex = 4242
	setPayload := fmt.Sprintf(`{"body":%q,"integratedTime":%d,"logID":%q,"logIndex":%d}`,
		base64.StdEncoding.EncodeToString([]byte(body)), integrated.Unix(), hex.EncodeToString(logID), logIndex)
	setHash := sha256.Sum256([]byte(setPayload))
	set, err := ecdsa.SignASN1(rand.Reader, logKey, setHash[:])
	if err != nil {
		t.Fatal(err)
	}
	if o.badSET {
		set[len(set)-1] ^= 0xff
	}

	// A three-leaf tree with the entry at index 2.
	l0 := sha256.Sum256([]byte{0, 'a'})
	l1 := sha256.Sum256([]byte{0, 'b'})
	l2 := sha256.Sum256(append([]byte{0}, body...))
	h01 := hashChildren(l0[:], l1[:])
	root := hashChildren(h01, l2[:])
	path := [][]byte{h01}
	if o.badProof {
		path = [][]byte{l0[:]}
	}
	cpText := fmt.Sprintf("test-log - 1\n3\n%s\n", base64.StdEncoding.EncodeToString(root))
	cpKey := logKey
	if o.badCheckpoint {
		cpKey = mustECKey(t, elliptic.P256())
	}
	cpHash := sha256.Sum256([]byte(cpText))
	cpSig, err := ecdsa.SignASN1(rand.Reader, cpKey, cpHash[:])
	if err != nil {
		t.Fatal(err)
	}
	envelope := cpText + "\n— test-log " + base64.StdEncoding.EncodeToString(append(append([]byte{}, logID[:4]...), cpSig...)) + "\n"

	kind := o.kind
	if kind == "" {
		kind = "hashedrekord"
	}
	entry := map[string]any{
		"logIndex":          strconv.Itoa(logIndex),
		"logId":             map[string]any{"keyId": logID},
		"kindVersion":       map[string]any{"kind": kind, "version": "0.0.1"},
		"integratedTime":    strconv.FormatInt(integrated.Unix(), 10),
		"canonicalizedBody": []byte(body),
	}
	if !o.noSET {
		entry["inclusionPromise"] = map[string]any{"signedEntryTimestamp": set}
	}
	if !o.noProof {
		entry["inclusionProof"] = map[string]any{
			"logIndex":   "2",
			"rootHash":   root,
			"treeSize":   "3",
			"hashes":     path,
			"checkpoint": map[string]any{"envelope": envelope},
		}
	}
	media := o.mediaType
	if media == "" {
		media = "application/vnd.dev.sigstore.bundle.v0.3+json"
	}
	vm := map[string]any{"tlogEntries": []any{entry}}
	if !o.noCert {
		vm["certificate"] = map[string]any{"rawBytes": leaf.Raw}
	}
	b := map[string]any{"mediaType": media, "verificationMaterial": vm}
	if !o.noMessageSig {
		b["messageSignature"] = map[string]any{
			"messageDigest": map[string]any{"algorithm": "SHA2_256", "digest": digest[:]},
			"signature":     sig,
		}
	}
	out, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// The verifier accepts the real signed checksums.txt of a published release
// with the production trust material, and pins it to that release's tag.
func TestVerifyChecksumsBundle_RealRelease(t *testing.T) {
	bundle, err := os.ReadFile(filepath.Join("testdata", "upgrade", "checksums.txt.bundle"))
	if err != nil {
		t.Fatal(err)
	}
	sums, err := os.ReadFile(filepath.Join("testdata", "upgrade", "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	trust := mustDefaultSigstoreTrust()
	if err := verifyChecksumsBundle(trust, bundle, sums, "v2.34.10"); err != nil {
		t.Fatalf("real v2.34.10 bundle rejected: %v", err)
	}
	if err := verifyChecksumsBundle(trust, bundle, sums, "v2.34.11"); err == nil || !strings.Contains(err.Error(), "signing identity") {
		t.Errorf("bundle accepted for another tag: %v", err)
	}
	tampered := append([]byte{}, sums...)
	tampered[0] ^= 1
	if err := verifyChecksumsBundle(trust, bundle, tampered, "v2.34.10"); err == nil {
		t.Error("tampered checksums.txt accepted")
	}
	// The real bundle under a foreign trust root must fail.
	if err := verifyChecksumsBundle(newTestSigstore(t).trust, bundle, sums, "v2.34.10"); err == nil {
		t.Error("real bundle accepted under an unrelated trust root")
	}
}

func TestVerifyChecksumsBundle_Synthetic(t *testing.T) {
	ts := newTestSigstore(t)
	artifact := []byte("0123  odek-linux-amd64\n")
	const tag = "v9.9.9"

	if err := verifyChecksumsBundle(ts.trust, ts.bundle(t, artifact, testBundleOpts{tag: tag}), artifact, tag); err != nil {
		t.Fatalf("valid bundle rejected: %v", err)
	}
	if err := verifyChecksumsBundle(ts.trust, ts.bundle(t, artifact, testBundleOpts{tag: tag, noProof: true}), artifact, tag); err != nil {
		t.Fatalf("valid bundle without inclusion proof rejected: %v", err)
	}
	if err := verifyChecksumsBundle(ts.trust, ts.bundle(t, artifact, testBundleOpts{tag: tag, legacyIssuer: true}), artifact, tag); err != nil {
		t.Fatalf("legacy-issuer certificate rejected: %v", err)
	}

	cases := []struct {
		name     string
		opts     testBundleOpts
		artifact []byte
		tag      string
		want     string
	}{
		{"tampered checksums", testBundleOpts{}, []byte("tampered"), tag, "digest"},
		{"wrong tag in SAN", testBundleOpts{san: upgradeWorkflow + "@refs/tags/v0.0.1"}, artifact, tag, "signing identity"},
		{"other workflow", testBundleOpts{san: upgradeSourceRepo + "/.github/workflows/evil.yml@refs/tags/" + tag}, artifact, tag, "signing identity"},
		{"branch ref", testBundleOpts{san: upgradeWorkflow + "@refs/heads/main"}, artifact, tag, "signing identity"},
		{"wrong issuer", testBundleOpts{issuer: "https://accounts.google.com"}, artifact, tag, "OIDC issuer"},
		{"wrong legacy issuer", testBundleOpts{issuer: "https://accounts.google.com", legacyIssuer: true}, artifact, tag, "OIDC issuer"},
		{"wrong source repo", testBundleOpts{repo: "https://github.com/evil/odek"}, artifact, tag, "source repository"},
		{"untrusted root", testBundleOpts{untrustedCA: true}, artifact, tag, "chain"},
		{"expired at integration", testBundleOpts{leafExpired: true}, artifact, tag, "chain"},
		{"bad SET", testBundleOpts{badSET: true}, artifact, tag, "signed entry timestamp"},
		{"no SET", testBundleOpts{noSET: true}, artifact, tag, "signed entry timestamp"},
		{"bad inclusion proof", testBundleOpts{badProof: true}, artifact, tag, "inclusion proof"},
		{"bad checkpoint", testBundleOpts{badCheckpoint: true}, artifact, tag, "checkpoint"},
		{"signature over other bytes", testBundleOpts{signOther: []byte("x")}, artifact, tag, "does not verify"},
		{"log body digest differs", testBundleOpts{bodyDigest: strings.Repeat("0", 64)}, artifact, tag, "different digest"},
		{"untrusted log", testBundleOpts{otherLogKey: true}, artifact, tag, "not trusted"},
		{"unsupported kind", testBundleOpts{kind: "intoto"}, artifact, tag, "unsupported"},
		{"media type", testBundleOpts{mediaType: "application/json"}, artifact, tag, "media type"},
		{"no message signature", testBundleOpts{noMessageSig: true}, artifact, tag, "message signature"},
		{"no certificate", testBundleOpts{noCert: true}, artifact, tag, "certificate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.opts.tag = tag
			err := verifyChecksumsBundle(ts.trust, ts.bundle(t, artifact, c.opts), c.artifact, c.tag)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want containing %q", err, c.want)
			}
		})
	}

	if err := verifyChecksumsBundle(ts.trust, []byte("{"), artifact, tag); err == nil {
		t.Error("malformed bundle accepted")
	}
	if err := verifyChecksumsBundle(nil, ts.bundle(t, artifact, testBundleOpts{tag: tag}), artifact, tag); err == nil {
		t.Error("nil trust accepted")
	}
}

// A body rewritten after logging (another certificate or signature) breaks
// the binding between the bundle and the log entry.
func TestVerifyChecksumsBundle_LogBodyBinding(t *testing.T) {
	ts := newTestSigstore(t)
	artifact := []byte("sums\n")
	const tag = "v1.2.3"
	a := ts.bundle(t, artifact, testBundleOpts{tag: tag})
	b := ts.bundle(t, artifact, testBundleOpts{tag: tag})
	var ba, bb map[string]any
	if err := json.Unmarshal(a, &ba); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &bb); err != nil {
		t.Fatal(err)
	}
	// Bundle a's certificate and signature with bundle b's log entry.
	ba["verificationMaterial"].(map[string]any)["tlogEntries"] = bb["verificationMaterial"].(map[string]any)["tlogEntries"]
	mixed, _ := json.Marshal(ba)
	if err := verifyChecksumsBundle(ts.trust, mixed, artifact, tag); err == nil || !strings.Contains(err.Error(), "different") {
		t.Fatalf("mixed bundle: err = %v", err)
	}
	// Two log entries are refused.
	vm := ba["verificationMaterial"].(map[string]any)
	vm["tlogEntries"] = []any{vm["tlogEntries"].([]any)[0], vm["tlogEntries"].([]any)[0]}
	two, _ := json.Marshal(ba)
	if err := verifyChecksumsBundle(ts.trust, two, artifact, tag); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("two entries: err = %v", err)
	}
}

func TestRFC6962RootFromPath(t *testing.T) {
	leaf := sha256.Sum256([]byte{0, 'x'})
	if _, err := rfc6962RootFromPath(0, 2, leaf[:], nil); err == nil {
		t.Error("short path accepted")
	}
	if _, err := rfc6962RootFromPath(0, 1, leaf[:], [][]byte{leaf[:]}); err == nil {
		t.Error("long path accepted")
	}
	if _, err := rfc6962RootFromPath(0, 2, leaf[:], [][]byte{{1, 2}}); err == nil {
		t.Error("bad hash length accepted")
	}
	got, err := rfc6962RootFromPath(0, 1, leaf[:], nil)
	if err != nil || !bytes.Equal(got, leaf[:]) {
		t.Errorf("single-leaf root = %x, %v", got, err)
	}
}

func TestVerifyCheckpoint_Malformed(t *testing.T) {
	ts := newTestSigstore(t)
	root := make([]byte, 32)
	for _, env := range []string{"", "origin\n3\n", "origin\n3\n\n— x y\n", "o\n4\n" + base64.StdEncoding.EncodeToString(root) + "\n\n— x zz\n"} {
		if err := verifyCheckpoint(&ts.rekorKey.PublicKey, ts.logID, env, 3, root); err == nil {
			t.Errorf("checkpoint %q accepted", env)
		}
	}
}

func TestPerformUpgrade_SkipSignatureInstallsUnsigned(t *testing.T) {
	newBin := []byte("new")
	srv := fakeReleaseServer(t, "v9.9.9", newBin, "")
	exe := filepath.Join(t.TempDir(), "odek")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := performUpgrade(context.Background(), srv.Client(), srv.URL, exe, "v1.0.0", upgradeOptions{skipSignature: true}, &out); err != nil {
		t.Fatalf("performUpgrade: %v", err)
	}
	if content, _ := os.ReadFile(exe); !bytes.Equal(content, newBin) {
		t.Errorf("binary not replaced: %q", content)
	}
	if !strings.Contains(out.String(), "signature verification skipped") {
		t.Errorf("output does not announce the skipped check: %q", out.String())
	}
}

func TestPerformUpgrade_VerifiesSignature(t *testing.T) {
	srv := fakeSignedReleaseServer(t, "v9.9.9", []byte("new"), "")
	exe := filepath.Join(t.TempDir(), "odek")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := performUpgrade(context.Background(), srv.Client(), srv.URL, exe, "v1.0.0", upgradeOptions{}, &out); err != nil {
		t.Fatalf("performUpgrade: %v", err)
	}
	if !strings.Contains(out.String(), "verified Sigstore signature: "+upgradeWorkflow+"@refs/tags/v9.9.9") {
		t.Errorf("output lacks verification line: %q", out.String())
	}
}

// A bundle signed for another release cannot vouch for this one, and a
// bundle signed outside the trusted CA is refused; neither installs.
func TestPerformUpgrade_RejectsForeignBundle(t *testing.T) {
	ts := newTestSigstore(t)
	ts.install(t)
	for name, opts := range map[string]testBundleOpts{
		"other tag":   {tag: "v9.9.8"},
		"untrusted":   {tag: "v9.9.9", untrustedCA: true},
		"tampered":    {tag: "v9.9.9", signOther: []byte("x")},
		"no promise":  {tag: "v9.9.9", noSET: true},
		"bad issuer":  {tag: "v9.9.9", issuer: "https://evil.example"},
		"bad logbody": {tag: "v9.9.9", bodyDigest: strings.Repeat("1", 64)},
	} {
		t.Run(name, func(t *testing.T) {
			srv := fakeReleaseServerWithBundle(t, "v9.9.9", []byte("evil"), "", func(sums []byte) []byte {
				return ts.bundle(t, sums, opts)
			})
			exe := filepath.Join(t.TempDir(), "odek")
			if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			_, err := performUpgrade(context.Background(), srv.Client(), srv.URL, exe, "v1.0.0", upgradeOptions{}, &out)
			if err == nil || !strings.Contains(err.Error(), "signature verification failed") {
				t.Fatalf("err = %v", err)
			}
			if content, _ := os.ReadFile(exe); string(content) != "old" {
				t.Errorf("binary replaced: %q", content)
			}
		})
	}
}

func TestPerformUpgrade_BundleDownloadFailure(t *testing.T) {
	// The release lists a bundle but serving it fails.
	srv := fakeReleaseServerWithBundle(t, "v9.9.9", []byte("new"), "", func([]byte) []byte { return nil })
	exe := filepath.Join(t.TempDir(), "odek")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	_, err := performUpgrade(context.Background(), srv.Client(), srv.URL, exe, "v1.0.0", upgradeOptions{}, &out)
	if err == nil || !strings.Contains(err.Error(), "cannot download "+checksumsBundleAsset) {
		t.Fatalf("err = %v", err)
	}
	if content, _ := os.ReadFile(exe); string(content) != "old" {
		t.Errorf("binary replaced: %q", content)
	}
}

func TestUpgradeCmd_UnknownFlagAndHelp(t *testing.T) {
	if err := upgradeCmd([]string{"--nope"}); err == nil {
		t.Error("unknown flag accepted")
	}
	if err := upgradeCmd([]string{"--skip-signature", "--help"}); err != nil {
		t.Errorf("--help: %v", err)
	}
}

func TestConventionalReleaseListsBundle(t *testing.T) {
	rel := conventionalRelease("v1.2.3")
	if !strings.HasSuffix(rel.assetURL(checksumsBundleAsset), "/v1.2.3/"+checksumsBundleAsset) {
		t.Errorf("bundle asset URL = %q", rel.assetURL(checksumsBundleAsset))
	}
}

func TestDefaultSigstoreTrust(t *testing.T) {
	trust := mustDefaultSigstoreTrust()
	const rekorLogID = "c0d23d6ad406973f9559f3ba2d1ca01f84147d8ffc5b8445c224f98b9591801d"
	if _, ok := trust.rekorKeys[rekorLogID]; !ok {
		t.Errorf("embedded Rekor key id missing; have %v", trust.rekorKeys)
	}
	if _, err := newSigstoreTrust([]string{"x"}, nil, nil); err == nil {
		t.Error("bad root accepted")
	}
	if _, err := newSigstoreTrust(nil, []string{"x"}, nil); err == nil {
		t.Error("bad intermediate accepted")
	}
	if _, err := newSigstoreTrust(nil, nil, []string{"x"}); err == nil {
		t.Error("bad key PEM accepted")
	}
	if _, err := newSigstoreTrust(nil, nil, []string{"-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----\n"}); err == nil {
		t.Error("bad key accepted")
	}
}
