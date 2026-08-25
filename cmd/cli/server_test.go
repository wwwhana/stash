package main

import "testing"

func TestConfiguredHTTPAddress(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantHost string
		wantPort string
	}{
		{name: "port only", raw: ":9107", wantHost: "0.0.0.0", wantPort: "9107"},
		{name: "host and port", raw: "127.0.0.1:9108", wantHost: "127.0.0.1", wantPort: "9108"},
		{name: "bare port", raw: "9109", wantHost: "0.0.0.0", wantPort: "9109"},
		{name: "invalid keeps fallback", raw: "not:an:address", wantHost: "0.0.0.0", wantPort: "9090"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port := configuredHTTPAddress(tt.raw, "0.0.0.0", "9090")
			if host != tt.wantHost || port != tt.wantPort {
				t.Fatalf("configuredHTTPAddress(%q) = (%q, %q), want (%q, %q)", tt.raw, host, port, tt.wantHost, tt.wantPort)
			}
		})
	}
}
