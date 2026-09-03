package main

import "testing"

func TestDisplayEnvValueNeverRevealsCredentials(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "STASH_OPENAI_API_KEY", value: "x", want: "<set>"},
		{name: "STASH_AUTH_API_SECRET", value: "secret-value", want: "<set>"},
		{name: "STASH_ADMIN_TOKEN", value: "token-value", want: "<set>"},
		{name: "STASH_POSTGRES_DSN", value: "postgresql://user:password@example/db", want: "<set>"},
		{name: "STASH_AUTH_API_SECRET", value: "", want: "<empty>"},
		{name: "STASH_LOG_LEVEL", value: "debug", want: "debug"},
	} {
		t.Run(test.name+test.want, func(t *testing.T) {
			if got := displayEnvValue(test.name, test.value); got != test.want {
				t.Fatalf("displayEnvValue(%q, value) = %q, want %q", test.name, got, test.want)
			}
		})
	}
}
