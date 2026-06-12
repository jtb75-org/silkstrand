package recon

import "testing"

func TestClassifyTarget(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"10.0.0.0/24", targetCIDR},
		{"10.0.0.0/8", targetCIDR},
		{"10.0.0.1-10.0.0.50", targetRange},
		{"10.0.0.5", targetIP},
		{"2001:db8::1", targetIP},
		{"app.example.com", targetHostname},
		{"my-app.example.com", targetHostname}, // hyphen must NOT read as a range
		{"a-b-c.internal", targetHostname},
		{"status.example.com", targetHostname},
		{"  app.example.com  ", targetHostname},
	}
	for _, c := range cases {
		if got := classifyTarget(c.in); got != c.want {
			t.Errorf("classifyTarget(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
