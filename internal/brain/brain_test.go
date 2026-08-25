package brain

import "testing"

func TestEscapeLikePattern(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"/foo_bar", `/foo\_bar`},
		{"/foo/bar", `/foo/bar`},
		{"/100%", `/100\%`},
		{`/a\b`, `/a\\b`},
		{"/foo_bar/baz_qux", `/foo\_bar/baz\_qux`},
	}
	for _, tt := range tests {
		if got := escapeLikePattern(tt.in); got != tt.want {
			t.Errorf("escapeLikePattern(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestLikePatternForDescendants(t *testing.T) {
	got := likePatternForDescendants("/foo_bar")
	want := `/foo\_bar/%`
	if got != want {
		t.Errorf("likePatternForDescendants(/foo_bar) = %q, want %q", got, want)
	}
}

func TestValidationRejectsAmbiguousInput(t *testing.T) {
	if err := validateContent(" \t\n"); err != ErrEmptyContent {
		t.Fatalf("whitespace content error = %v, want ErrEmptyContent", err)
	}
	if err := validatePath("/projects/"); err != ErrInvalidPath {
		t.Fatalf("trailing slash path error = %v, want ErrInvalidPath", err)
	}
	if err := validateConfidence(1.1); err != ErrInvalidConfidence {
		t.Fatalf("invalid confidence error = %v, want ErrInvalidConfidence", err)
	}
	if err := validateScore(-0.1); err != ErrInvalidScore {
		t.Fatalf("invalid score error = %v, want ErrInvalidScore", err)
	}
}

func TestPaginationHonorsConfiguredResultLimit(t *testing.T) {
	got := (Pagination{Limit: 500, Offset: -2}).SanitizeWithMax(25)
	if got.Limit != 25 || got.Offset != 0 {
		t.Fatalf("sanitized page = %#v, want limit 25 and offset 0", got)
	}
	if got := (Pagination{Limit: 10}).SanitizeWithMax(0); got.Limit != 10 {
		t.Fatalf("unconfigured max changed page limit to %d", got.Limit)
	}
}
