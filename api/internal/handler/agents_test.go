package handler

import "testing"

func TestDiscoverScheduleToCron(t *testing.T) {
	cases := []struct {
		in      string
		wantNil bool
		want    string
		wantErr bool
	}{
		{"", true, "", false},
		{"off", true, "", false},
		{"OFF", true, "", false},
		{"daily", false, "0 3 * * *", false},
		{" Daily ", false, "0 3 * * *", false},
		{"weekly", false, "0 3 * * 1", false},
		{"hourly", false, "", true},
		{"garbage", false, "", true},
	}
	for _, c := range cases {
		got, err := discoverScheduleToCron(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: expected error, got nil", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error %v", c.in, err)
			continue
		}
		if c.wantNil {
			if got != nil {
				t.Errorf("%q: want nil cron, got %q", c.in, *got)
			}
			continue
		}
		if got == nil || *got != c.want {
			t.Errorf("%q: got %v want %q", c.in, got, c.want)
		}
	}
}
