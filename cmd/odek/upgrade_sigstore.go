package main

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Sigstore verification of the release checksums.txt.
//
// release.yml signs checksums.txt with keyless cosign: a short-lived Fulcio
// certificate bound to the workflow's GitHub OIDC identity, logged in the
// Rekor transparency log, shipped as a Sigstore bundle (checksums.txt.bundle).
// The verifier below checks that bundle with the standard library only:
//
//  1. the leaf certificate chains to the embedded Fulcio root, evaluated at
//     the Rekor-attested integration time (Fulcio leaves live ten minutes);
//  2. the certificate identity is this repository's release workflow at the
//     exact tag being installed, issued through GitHub Actions OIDC;
//  3. the message signature verifies over checksums.txt and its digest
//     matches the downloaded file;
//  4. the Rekor entry (hashedrekord) records that same digest, signature and
//     certificate, its signed entry timestamp verifies against the embedded
//     Rekor key, and its inclusion proof, when present, hashes to the root of
//     a checkpoint signed by that key.
//
// Every failure refuses the upgrade. The trust material is compiled in, so a
// Sigstore root rotation leaves an older binary unable to verify newer
// releases; `odek upgrade --skip-signature` falls back to checksum-only.

const (
	upgradeOIDCIssuer = "https://token.actions.githubusercontent.com"
	upgradeSourceRepo = "https://github.com/" + githubRepo
	upgradeWorkflow   = upgradeSourceRepo + "/.github/workflows/release.yml"
)

// Sigstore public-good trust material from the root-signing trusted_root.json
// (Fulcio CA valid from 2022-04-13; Rekor log rekor.sigstore.dev).
const (
	sigstoreFulcioIntermediatePEM = `-----BEGIN CERTIFICATE-----
MIICGjCCAaGgAwIBAgIUALnViVfnU0brJasmRkHrn/UnfaQwCgYIKoZIzj0EAwMw
KjEVMBMGA1UEChMMc2lnc3RvcmUuZGV2MREwDwYDVQQDEwhzaWdzdG9yZTAeFw0y
MjA0MTMyMDA2MTVaFw0zMTEwMDUxMzU2NThaMDcxFTATBgNVBAoTDHNpZ3N0b3Jl
LmRldjEeMBwGA1UEAxMVc2lnc3RvcmUtaW50ZXJtZWRpYXRlMHYwEAYHKoZIzj0C
AQYFK4EEACIDYgAE8RVS/ysH+NOvuDZyPIZtilgUF9NlarYpAd9HP1vBBH1U5CV7
7LSS7s0ZiH4nE7Hv7ptS6LvvR/STk798LVgMzLlJ4HeIfF3tHSaexLcYpSASr1kS
0N/RgBJz/9jWCiXno3sweTAOBgNVHQ8BAf8EBAMCAQYwEwYDVR0lBAwwCgYIKwYB
BQUHAwMwEgYDVR0TAQH/BAgwBgEB/wIBADAdBgNVHQ4EFgQU39Ppz1YkEZb5qNjp
KFWixi4YZD8wHwYDVR0jBBgwFoAUWMAeX5FFpWapesyQoZMi0CrFxfowCgYIKoZI
zj0EAwMDZwAwZAIwPCsQK4DYiZYDPIaDi5HFKnfxXx6ASSVmERfsynYBiX2X6SJR
nZU84/9DZdnFvvxmAjBOt6QpBlc4J/0DxvkTCqpclvziL6BCCPnjdlIB3Pu3BxsP
mygUY7Ii2zbdCdliiow=
-----END CERTIFICATE-----
`
	sigstoreFulcioRootPEM = `-----BEGIN CERTIFICATE-----
MIIB9zCCAXygAwIBAgIUALZNAPFdxHPwjeDloDwyYChAO/4wCgYIKoZIzj0EAwMw
KjEVMBMGA1UEChMMc2lnc3RvcmUuZGV2MREwDwYDVQQDEwhzaWdzdG9yZTAeFw0y
MTEwMDcxMzU2NTlaFw0zMTEwMDUxMzU2NThaMCoxFTATBgNVBAoTDHNpZ3N0b3Jl
LmRldjERMA8GA1UEAxMIc2lnc3RvcmUwdjAQBgcqhkjOPQIBBgUrgQQAIgNiAAT7
XeFT4rb3PQGwS4IajtLk3/OlnpgangaBclYpsYBr5i+4ynB07ceb3LP0OIOZdxex
X69c5iVuyJRQ+Hz05yi+UF3uBWAlHpiS5sh0+H2GHE7SXrk1EC5m1Tr19L9gg92j
YzBhMA4GA1UdDwEB/wQEAwIBBjAPBgNVHRMBAf8EBTADAQH/MB0GA1UdDgQWBBRY
wB5fkUWlZql6zJChkyLQKsXF+jAfBgNVHSMEGDAWgBRYwB5fkUWlZql6zJChkyLQ
KsXF+jAKBggqhkjOPQQDAwNpADBmAjEAj1nHeXZp+13NWBNa+EDsDP8G1WWg1tCM
WP/WHPqpaVo0jhsweNFZgSs0eE7wYI4qAjEA2WB9ot98sIkoF3vZYdd3/VtWB5b9
TNMea7Ix/stJ5TfcLLeABLE4BNJOsQ4vnBHJ
-----END CERTIFICATE-----
`
	sigstoreRekorKeyPEM = `-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE2G2Y+2tabdTV5BcGiBIx0a9fAFwr
kBbmLSGtks4L3qX6yYY0zufBnhC8Ur/iy55GhWP/9A/bY2LhC30M9+RYtw==
-----END PUBLIC KEY-----
`
)

// Fulcio certificate extensions (OID arc 1.3.6.1.4.1.57264.1).
var (
	oidFulcioIssuerLegacy = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 1}
	oidFulcioIssuer       = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 8}
	oidFulcioSourceRepo   = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 12}
)

// sigstoreTrust is the trust material the verifier checks against.
type sigstoreTrust struct {
	roots         *x509.CertPool
	intermediates *x509.CertPool
	// rekorKeys maps a Rekor log ID (hex SHA-256 of the DER public key) to
	// that log's public key.
	rekorKeys map[string]crypto.PublicKey
}

// upgradeTrust is the trust material used by performUpgrade. Tests swap it
// for an in-test CA and log key.
var upgradeTrust = mustDefaultSigstoreTrust()

func mustDefaultSigstoreTrust() *sigstoreTrust {
	t, err := newSigstoreTrust([]string{sigstoreFulcioRootPEM}, []string{sigstoreFulcioIntermediatePEM}, []string{sigstoreRekorKeyPEM})
	if err != nil {
		panic("embedded Sigstore trust material is invalid: " + err.Error())
	}
	return t
}

func newSigstoreTrust(rootPEMs, intermediatePEMs, rekorKeyPEMs []string) (*sigstoreTrust, error) {
	t := &sigstoreTrust{roots: x509.NewCertPool(), intermediates: x509.NewCertPool(), rekorKeys: map[string]crypto.PublicKey{}}
	for _, p := range rootPEMs {
		if !t.roots.AppendCertsFromPEM([]byte(p)) {
			return nil, errors.New("bad root certificate")
		}
	}
	for _, p := range intermediatePEMs {
		if !t.intermediates.AppendCertsFromPEM([]byte(p)) {
			return nil, errors.New("bad intermediate certificate")
		}
	}
	for _, p := range rekorKeyPEMs {
		blk, _ := pem.Decode([]byte(p))
		if blk == nil {
			return nil, errors.New("bad Rekor key PEM")
		}
		pub, err := x509.ParsePKIXPublicKey(blk.Bytes)
		if err != nil {
			return nil, fmt.Errorf("bad Rekor key: %w", err)
		}
		id := sha256.Sum256(blk.Bytes)
		t.rekorKeys[hex.EncodeToString(id[:])] = pub
	}
	return t, nil
}

// sigstoreBundle is the subset of a Sigstore bundle (v0.3) the verifier reads.
type sigstoreBundle struct {
	MediaType            string `json:"mediaType"`
	VerificationMaterial struct {
		Certificate *struct {
			RawBytes []byte `json:"rawBytes"`
		} `json:"certificate"`
		X509CertificateChain *struct {
			Certificates []struct {
				RawBytes []byte `json:"rawBytes"`
			} `json:"certificates"`
		} `json:"x509CertificateChain"`
		TlogEntries []sigstoreTlogEntry `json:"tlogEntries"`
	} `json:"verificationMaterial"`
	MessageSignature *struct {
		MessageDigest struct {
			Algorithm string `json:"algorithm"`
			Digest    []byte `json:"digest"`
		} `json:"messageDigest"`
		Signature []byte `json:"signature"`
	} `json:"messageSignature"`
}

type sigstoreTlogEntry struct {
	LogIndex string `json:"logIndex"`
	LogID    struct {
		KeyID []byte `json:"keyId"`
	} `json:"logId"`
	KindVersion struct {
		Kind    string `json:"kind"`
		Version string `json:"version"`
	} `json:"kindVersion"`
	IntegratedTime   string `json:"integratedTime"`
	InclusionPromise *struct {
		SignedEntryTimestamp []byte `json:"signedEntryTimestamp"`
	} `json:"inclusionPromise"`
	InclusionProof *struct {
		LogIndex   string   `json:"logIndex"`
		RootHash   []byte   `json:"rootHash"`
		TreeSize   string   `json:"treeSize"`
		Hashes     [][]byte `json:"hashes"`
		Checkpoint struct {
			Envelope string `json:"envelope"`
		} `json:"checkpoint"`
	} `json:"inclusionProof"`
	CanonicalizedBody []byte `json:"canonicalizedBody"`
}

// hashedRekordBody is the canonicalized Rekor entry body of kind
// hashedrekord, version 0.0.1.
type hashedRekordBody struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Spec       struct {
		Data struct {
			Hash struct {
				Algorithm string `json:"algorithm"`
				Value     string `json:"value"`
			} `json:"hash"`
		} `json:"data"`
		Signature struct {
			Content   []byte `json:"content"`
			PublicKey struct {
				Content []byte `json:"content"`
			} `json:"publicKey"`
		} `json:"signature"`
	} `json:"spec"`
}

// verifyChecksumsBundle verifies that bundleJSON is a valid Sigstore
// signature over artifact, made by this repository's release workflow for
// tag. It returns nil only when every check passes.
func verifyChecksumsBundle(trust *sigstoreTrust, bundleJSON, artifact []byte, tag string) error {
	if trust == nil {
		return errors.New("no Sigstore trust material")
	}
	var b sigstoreBundle
	if err := json.Unmarshal(bundleJSON, &b); err != nil {
		return fmt.Errorf("cannot parse bundle: %w", err)
	}
	if !strings.HasPrefix(b.MediaType, "application/vnd.dev.sigstore.bundle") {
		return fmt.Errorf("unsupported bundle media type %q", b.MediaType)
	}
	if b.MessageSignature == nil {
		return errors.New("bundle has no message signature")
	}

	leafDER, err := bundleLeafDER(&b)
	if err != nil {
		return err
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		return fmt.Errorf("cannot parse signing certificate: %w", err)
	}

	// The message digest must be the artifact's, and the signature must
	// verify over it with the certificate's key.
	digest := sha256.Sum256(artifact)
	if b.MessageSignature.MessageDigest.Algorithm != "SHA2_256" ||
		!bytes.Equal(b.MessageSignature.MessageDigest.Digest, digest[:]) {
		return errors.New("bundle digest does not match checksums.txt")
	}
	if err := verifyDigestSignature(leaf.PublicKey, digest[:], b.MessageSignature.Signature); err != nil {
		return fmt.Errorf("signature over checksums.txt does not verify: %w", err)
	}

	if len(b.VerificationMaterial.TlogEntries) != 1 {
		return fmt.Errorf("bundle must carry exactly one transparency-log entry, has %d", len(b.VerificationMaterial.TlogEntries))
	}
	integrated, err := verifyTlogEntry(trust, &b.VerificationMaterial.TlogEntries[0], digest[:], b.MessageSignature.Signature, leafDER)
	if err != nil {
		return err
	}

	// Chain to the Fulcio root at the time the log attested the entry.
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         trust.roots,
		Intermediates: trust.intermediates,
		CurrentTime:   integrated,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
	}); err != nil {
		return fmt.Errorf("signing certificate does not chain to the Sigstore root: %w", err)
	}
	return verifyReleaseIdentity(leaf, tag)
}

func bundleLeafDER(b *sigstoreBundle) ([]byte, error) {
	vm := b.VerificationMaterial
	switch {
	case vm.Certificate != nil && len(vm.Certificate.RawBytes) > 0:
		return vm.Certificate.RawBytes, nil
	case vm.X509CertificateChain != nil && len(vm.X509CertificateChain.Certificates) > 0:
		return vm.X509CertificateChain.Certificates[0].RawBytes, nil
	}
	return nil, errors.New("bundle has no signing certificate")
}

// verifyReleaseIdentity pins the certificate to the release workflow of this
// repository at exactly tag, issued through GitHub Actions OIDC.
func verifyReleaseIdentity(leaf *x509.Certificate, tag string) error {
	want := upgradeWorkflow + "@refs/tags/" + tag
	if len(leaf.URIs) != 1 || leaf.URIs[0].String() != want {
		got := make([]string, 0, len(leaf.URIs))
		for _, u := range leaf.URIs {
			got = append(got, u.String())
		}
		return fmt.Errorf("signing identity %q is not %q", strings.Join(got, ","), want)
	}
	if len(leaf.EmailAddresses) != 0 || len(leaf.DNSNames) != 0 || len(leaf.IPAddresses) != 0 {
		return errors.New("signing certificate carries unexpected identities")
	}
	issuer, err := fulcioExtension(leaf, oidFulcioIssuer, true)
	if err != nil || issuer == "" {
		issuer, err = fulcioExtension(leaf, oidFulcioIssuerLegacy, false)
	}
	if err != nil || issuer != upgradeOIDCIssuer {
		return fmt.Errorf("signing certificate OIDC issuer %q is not %q", issuer, upgradeOIDCIssuer)
	}
	// Older Fulcio certificates lack the source-repository extension; when
	// present it must name this repository.
	if repo, err := fulcioExtension(leaf, oidFulcioSourceRepo, true); err != nil {
		return fmt.Errorf("signing certificate source repository: %w", err)
	} else if repo != "" && repo != upgradeSourceRepo {
		return fmt.Errorf("signing certificate source repository %q is not %q", repo, upgradeSourceRepo)
	}
	return nil
}

// fulcioExtension returns the string value of the extension oid, or "" when
// absent. derString selects the DER UTF8String encoding used by the newer
// Fulcio extensions; the legacy ones carry raw bytes.
func fulcioExtension(cert *x509.Certificate, oid asn1.ObjectIdentifier, derString bool) (string, error) {
	for _, ext := range cert.Extensions {
		if !ext.Id.Equal(oid) {
			continue
		}
		if !derString {
			return string(ext.Value), nil
		}
		var s string
		rest, err := asn1.UnmarshalWithParams(ext.Value, &s, "utf8")
		if err != nil {
			return "", err
		}
		if len(rest) != 0 {
			return "", errors.New("trailing data in extension")
		}
		return s, nil
	}
	return "", nil
}

// verifyDigestSignature verifies an ASN.1 ECDSA signature over a SHA-256
// digest (the hashedrekord signing scheme cosign uses for blobs).
func verifyDigestSignature(pub crypto.PublicKey, digest, sig []byte) error {
	k, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return fmt.Errorf("unsupported key type %T", pub)
	}
	if !ecdsa.VerifyASN1(k, digest, sig) {
		return errors.New("invalid ECDSA signature")
	}
	return nil
}

// verifyTlogEntry checks the Rekor entry and returns its integration time.
func verifyTlogEntry(trust *sigstoreTrust, e *sigstoreTlogEntry, digest, sig, leafDER []byte) (time.Time, error) {
	if e.KindVersion.Kind != "hashedrekord" || e.KindVersion.Version != "0.0.1" {
		return time.Time{}, fmt.Errorf("unsupported transparency-log entry %s/%s", e.KindVersion.Kind, e.KindVersion.Version)
	}
	logID := hex.EncodeToString(e.LogID.KeyID)
	logKey, ok := trust.rekorKeys[logID]
	if !ok {
		return time.Time{}, fmt.Errorf("transparency log %s is not trusted", logID)
	}

	// The entry must record exactly this digest, signature and certificate.
	var body hashedRekordBody
	if err := json.Unmarshal(e.CanonicalizedBody, &body); err != nil {
		return time.Time{}, fmt.Errorf("cannot parse transparency-log entry: %w", err)
	}
	if body.Kind != "hashedrekord" || body.APIVersion != "0.0.1" {
		return time.Time{}, errors.New("transparency-log entry body is not hashedrekord 0.0.1")
	}
	if body.Spec.Data.Hash.Algorithm != "sha256" || !strings.EqualFold(body.Spec.Data.Hash.Value, hex.EncodeToString(digest)) {
		return time.Time{}, errors.New("transparency-log entry records a different digest")
	}
	if !bytes.Equal(body.Spec.Signature.Content, sig) {
		return time.Time{}, errors.New("transparency-log entry records a different signature")
	}
	blk, _ := pem.Decode(body.Spec.Signature.PublicKey.Content)
	if blk == nil || blk.Type != "CERTIFICATE" || !bytes.Equal(blk.Bytes, leafDER) {
		return time.Time{}, errors.New("transparency-log entry records a different certificate")
	}

	logIndex, err := strconv.ParseInt(e.LogIndex, 10, 64)
	if err != nil || logIndex < 0 {
		return time.Time{}, fmt.Errorf("bad log index %q", e.LogIndex)
	}
	integratedTime, err := strconv.ParseInt(e.IntegratedTime, 10, 64)
	if err != nil || integratedTime <= 0 {
		return time.Time{}, fmt.Errorf("bad integrated time %q", e.IntegratedTime)
	}

	// The signed entry timestamp is the log's promise over body, time and
	// index; it is what makes the integration time trustworthy.
	if e.InclusionPromise == nil || len(e.InclusionPromise.SignedEntryTimestamp) == 0 {
		return time.Time{}, errors.New("transparency-log entry has no signed entry timestamp")
	}
	setPayload, err := json.Marshal(struct {
		Body           string `json:"body"`
		IntegratedTime int64  `json:"integratedTime"`
		LogID          string `json:"logID"`
		LogIndex       int64  `json:"logIndex"`
	}{base64.StdEncoding.EncodeToString(e.CanonicalizedBody), integratedTime, logID, logIndex})
	if err != nil {
		return time.Time{}, err
	}
	if err := verifyLogSignature(logKey, setPayload, e.InclusionPromise.SignedEntryTimestamp); err != nil {
		return time.Time{}, fmt.Errorf("signed entry timestamp does not verify: %w", err)
	}

	if e.InclusionProof != nil {
		if err := verifyInclusionProof(logKey, e, e.CanonicalizedBody); err != nil {
			return time.Time{}, fmt.Errorf("inclusion proof: %w", err)
		}
	}
	return time.Unix(integratedTime, 0), nil
}

// verifyLogSignature verifies a log signature over msg (SHA-256 for ECDSA,
// the raw message for Ed25519).
func verifyLogSignature(pub crypto.PublicKey, msg, sig []byte) error {
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		h := sha256.Sum256(msg)
		if !ecdsa.VerifyASN1(k, h[:], sig) {
			return errors.New("invalid ECDSA signature")
		}
	case ed25519.PublicKey:
		if !ed25519.Verify(k, msg, sig) {
			return errors.New("invalid Ed25519 signature")
		}
	default:
		return fmt.Errorf("unsupported log key type %T", pub)
	}
	return nil
}

// verifyInclusionProof checks the RFC 6962 audit path from the entry's leaf
// to the proof's root hash and that the root is the one in a checkpoint
// signed by the log.
func verifyInclusionProof(logKey crypto.PublicKey, e *sigstoreTlogEntry, body []byte) error {
	p := e.InclusionProof
	index, err := strconv.ParseUint(p.LogIndex, 10, 64)
	if err != nil {
		return fmt.Errorf("bad index %q", p.LogIndex)
	}
	size, err := strconv.ParseUint(p.TreeSize, 10, 64)
	if err != nil || index >= size {
		return fmt.Errorf("bad tree size %q for index %d", p.TreeSize, index)
	}
	leaf := sha256.Sum256(append([]byte{0}, body...))
	root, err := rfc6962RootFromPath(index, size, leaf[:], p.Hashes)
	if err != nil {
		return err
	}
	if !bytes.Equal(root, p.RootHash) {
		return errors.New("audit path does not hash to the root")
	}
	return verifyCheckpoint(logKey, e.LogID.KeyID, p.Checkpoint.Envelope, size, p.RootHash)
}

// rfc6962RootFromPath computes the Merkle tree root from a leaf hash and its
// audit path (RFC 9162 section 2.1.3.2).
func rfc6962RootFromPath(index, size uint64, leaf []byte, path [][]byte) ([]byte, error) {
	fn, sn := index, size-1
	r := leaf
	for _, p := range path {
		if len(p) != sha256.Size {
			return nil, errors.New("bad audit path hash")
		}
		if sn == 0 {
			return nil, errors.New("audit path too long")
		}
		if fn&1 == 1 || fn == sn {
			r = hashChildren(p, r)
			for fn&1 == 0 && fn != 0 {
				fn >>= 1
				sn >>= 1
			}
		} else {
			r = hashChildren(r, p)
		}
		fn >>= 1
		sn >>= 1
	}
	if sn != 0 {
		return nil, errors.New("audit path too short")
	}
	return r, nil
}

func hashChildren(l, r []byte) []byte {
	h := sha256.New()
	h.Write([]byte{1})
	h.Write(l)
	h.Write(r)
	return h.Sum(nil)
}

// verifyCheckpoint verifies a signed-note checkpoint ("origin\nsize\nroot\n",
// a blank line, then "— name base64(keyhint||sig)" lines): one signature
// whose key hint matches the log ID must verify, and the checkpoint must
// commit to size and root.
func verifyCheckpoint(logKey crypto.PublicKey, logID []byte, envelope string, size uint64, root []byte) error {
	text, sigs, ok := strings.Cut(envelope, "\n\n")
	if !ok {
		return errors.New("malformed checkpoint")
	}
	text += "\n"
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) < 3 {
		return errors.New("malformed checkpoint body")
	}
	if lines[1] != strconv.FormatUint(size, 10) || lines[2] != base64.StdEncoding.EncodeToString(root) {
		return errors.New("checkpoint does not commit to the proof's tree")
	}
	for _, line := range strings.Split(sigs, "\n") {
		if !strings.HasPrefix(line, "— ") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "— "))
		if len(fields) != 2 {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(fields[1])
		if err != nil || len(raw) < 5 || len(logID) < 4 || !bytes.Equal(raw[:4], logID[:4]) {
			continue
		}
		if verifyLogSignature(logKey, []byte(text), raw[4:]) == nil {
			return nil
		}
	}
	return errors.New("no valid log signature on checkpoint")
}
