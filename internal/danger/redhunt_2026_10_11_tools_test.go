package danger

import "testing"

func TestHTTPRequestClassTools(t *testing.T) {
	cases := []struct {
		method  string
		headers map[string]string
		want    RiskClass
	}{
		{"", nil, NetworkEgress},
		{"GET", map[string]string{"Accept": "*/*", "User-Agent": "x"}, NetworkEgress},
		{"head", nil, NetworkEgress},
		{"OPTIONS", nil, NetworkEgress},
		{"TRACE", nil, NetworkEgress},
		{"POST", nil, NetworkUpload},
		{"delete", nil, NetworkUpload},
		{"FROB", nil, NetworkUpload},
		{"GET", map[string]string{"Authorization": "Bearer x"}, NetworkUpload},
		{"GET", map[string]string{"proxy-authorization": "x"}, NetworkUpload},
		{"GET", map[string]string{"Cookie": "a=b"}, NetworkUpload},
		{"GET", map[string]string{"X-Api-Key": "k"}, NetworkUpload},
		{"GET", map[string]string{"x-goog-api-key": "k"}, NetworkUpload},
		{"GET", map[string]string{"Private-Token": "k"}, NetworkUpload},
		{"GET", map[string]string{"X-Amz-Security-Token": "k"}, NetworkUpload},
		{"GET", map[string]string{"Key": "k"}, NetworkUpload},
		{"GET", map[string]string{"X-Client_Key": "k"}, NetworkUpload},
	}
	for _, c := range cases {
		if got := HTTPRequestClass(c.method, c.headers); got != c.want {
			t.Errorf("HTTPRequestClass(%q, %v) = %q, want %q", c.method, c.headers, got, c.want)
		}
	}
}
