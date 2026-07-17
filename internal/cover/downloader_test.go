package cover

import "testing"

func TestAllowedHost(t *testing.T) {
	for _, host := range []string{"cdn.truyenonl.net", "tangthuvien.org", "images.tangthuvien.org"} {
		if !allowedHost(host) {
			t.Fatalf("host %q should be allowed", host)
		}
	}
	if allowedHost("example.com") {
		t.Fatal("untrusted host was allowed")
	}
}

func TestSafeName(t *testing.T) {
	if got := safeName("book-slug_2026"); got != "book-slug_2026" {
		t.Fatalf("safe name=%q", got)
	}
}

func TestExtension(t *testing.T) {
	if got := extension("image/jpeg; charset=binary", []byte("not-used")); got != ".jpg" {
		t.Fatalf("extension=%q", got)
	}
	if got := extension("text/html", []byte("<html>")); got != "" {
		t.Fatalf("non-image extension=%q", got)
	}
}
