package handlers

import "testing"

// New parses every page template and panics on a syntax error, which would stop the server at startup.
func TestTemplatesParse(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("a template failed to parse: %v", r)
		}
	}()
	New(Deps{})
}
