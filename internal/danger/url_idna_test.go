package danger

import "testing"

func TestClassifyURL_MapsUnicodeHosts(t *testing.T) {
	for _, u := range []string{
		"http://１２７.０.０.１:8080/", "http://ｌｏｃａｌｈｏｓｔ:8080", "http://１６９.２５４.１６９.２５４/latest/",
	} {
		if got := ClassifyURL(u); got != SystemWrite {
			t.Errorf("ClassifyURL(%q) = %v, want system_write", u, got)
		}
	}
	if got := ClassifyURL("https://bücher.example/"); got != NetworkEgress {
		t.Errorf("ordinary IDN = %v, want network_egress", got)
	}
}
