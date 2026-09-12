package main

import (
	"os"
	"strings"
	"testing"
)

func TestNormalizeOnionURL(t *testing.T) {
	valid := "http://abcdefghijklmnop.onion/path#fragment"
	got := normalizeOnionURL(valid)
	want := "http://abcdefghijklmnop.onion/path"
	if got != want {
		t.Fatalf("normalizeOnionURL() = %q, want %q", got, want)
	}
	if normalizeOnionURL("https://example.com") != "" {
		t.Fatal("clearnet URL should be rejected")
	}
}

func TestParseProviderHTML(t *testing.T) {
	html := []byte(`<a href="http://abcdefghijklmnop.onion/">Example One</a><a href="https://abcdefghijklmnop.onion/">Duplicate</a><a href="https://example.com/">Nope</a>`)
	results := parseProviderHTML(html)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Title != "Example One" {
		t.Fatalf("got title %q", results[0].Title)
	}
}

func TestParseProviderHTMLFindsBareOnionURL(t *testing.T) {
	html := []byte(`Some result: https://abcdefghijklmnop.onion/test?q=1`)
	results := parseProviderHTML(html)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].URL != "https://abcdefghijklmnop.onion/test?q=1" {
		t.Fatalf("got URL %q", results[0].URL)
	}
}

func TestScoreResult(t *testing.T) {
	top := result{Title: "Tor Search Portal", URL: "http://abcdefghijklmnop.onion/"}
	low := result{Title: "Unrelated", URL: "http://qrstuvwxyzabcdef.onion/"}
	if scoreResult([]string{"tor"}, top) <= scoreResult([]string{"tor"}, low) {
		t.Fatal("matching title should rank higher")
	}
}

func TestEmbeddedFrontPageCSS(t *testing.T) {
	data, err := os.ReadFile("../index.html")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, `href="styles/style.css"`) {
		t.Fatal("front page still references external style.css")
	}
	if !strings.Contains(text, "<style>") || !strings.Contains(text, "background-color: #EDEBEB") {
		t.Fatal("css error")
	}
}
