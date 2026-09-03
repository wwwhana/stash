package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestExplicitConsolidationNamespaces(t *testing.T) {
	tests := []struct {
		name    string
		values  []string
		want    []string
		wantErr string
	}{
		{name: "missing", wantErr: "at least one"},
		{name: "root", values: []string{"/"}, wantErr: "is not allowed"},
		{name: "relative", values: []string{"projects/stash"}, wantErr: "must start with /"},
		{name: "comma separated and deduplicated", values: []string{"/projects/stash, /projects/stash", "/projects/other"}, want: []string{"/projects/stash", "/projects/other"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := explicitConsolidationNamespaces(tt.values)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want text %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("explicitConsolidationNamespaces: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("namespaces = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestConsolidateToolRequiresExplicitNamespaces(t *testing.T) {
	tool := newMCPServer(nil).GetTool("consolidate")
	if tool == nil {
		t.Fatal("consolidate tool is not registered")
	}
	found := false
	for _, field := range tool.Tool.InputSchema.Required {
		if field == "namespaces" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("consolidate required fields = %v; want namespaces", tool.Tool.InputSchema.Required)
	}
	if !strings.Contains(strings.ToLower(tool.Tool.Description), "non-root") {
		t.Fatalf("consolidate description does not reject root scope: %q", tool.Tool.Description)
	}
}
