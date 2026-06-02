package main

import (
	"reflect"
	"testing"
)

func TestExtractWordTokens(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"ax check generalize platform", []string{"ax", "check", "generalize", "platform"}},
		{"sp-ucp-manifest llms-txt", []string{"sp-ucp-manifest", "llms-txt"}},
		{"branded_accuracy|rank_for_merchant|sources dict",
			[]string{"branded_accuracy", "rank_for_merchant", "sources", "dict"}},
		{"foo foo foo", []string{"foo"}},          // dedup
		{"a x .*", nil},                            // all tokens <2 chars / metachars → none
		{"singleword", []string{"singleword"}},     // 1 token (caller decides ≥2)
	}
	for _, c := range cases {
		got := extractWordTokens(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("extractWordTokens(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestContainsAllTokens(t *testing.T) {
	data := []byte("The UCP manifest and the JSONLD presence check")
	all := [][]byte{[]byte("ucp"), []byte("jsonld")}
	if !containsAllTokens(data, all) {
		t.Error("expected all tokens present (case-insensitive)")
	}
	missing := [][]byte{[]byte("ucp"), []byte("missingtoken")}
	if containsAllTokens(data, missing) {
		t.Error("expected false when a token is absent")
	}
}
