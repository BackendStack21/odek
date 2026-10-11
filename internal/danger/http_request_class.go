package danger

import "strings"

// HTTPRequestClass classifies a structured HTTP request (the http_request
// tool) by the same rules the shell classifier applies to curl: a mutating
// method (anything other than GET, HEAD, OPTIONS or TRACE; an unknown
// spelling fails closed) or a credential-bearing header makes the request
// NetworkUpload, otherwise it is NetworkEgress. The destination URL is
// classified separately (ClassifyURL); both verdicts apply.
func HTTPRequestClass(method string, headers map[string]string) RiskClass {
	if strings.TrimSpace(method) != "" && mutatingMethod(method) {
		return NetworkUpload
	}
	for name := range headers {
		if CredentialHeaderName(name) {
			return NetworkUpload
		}
	}
	return NetworkEgress
}

// credentialHeaderFragments are header-name fragments that mark a request
// header as carrying credentials or session state.
var credentialHeaderFragments = []string{
	"auth", "token", "secret", "password", "passwd", "cookie", "session",
	"credential", "signature", "apikey", "api-key", "api_key",
}

// CredentialHeaderName reports whether an HTTP header name carries
// credentials (Authorization, Cookie, X-Api-Key, Private-Token, ...). The
// match is deliberately broad: a false positive costs one prompt, a false
// negative sends a credential unprompted.
func CredentialHeaderName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, frag := range credentialHeaderFragments {
		if strings.Contains(n, frag) {
			return true
		}
	}
	return strings.HasSuffix(n, "-key") || strings.HasSuffix(n, "_key") || n == "key"
}
