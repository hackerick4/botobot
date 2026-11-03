package site

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every botobot.app address printed by the CLI, the prompts, or the published
// files must be a real route (v0.4.8: the help once pointed at a 404).
func TestPrintedURLsAreRoutes(t *testing.T) {
	served := map[string]bool{"/": true, "/join": true}
	for _, p := range Pages {
		served[p.Path] = true
	}
	dynamic := func(p string) bool {
		return strings.HasPrefix(p, "/b/br_") || strings.HasPrefix(p, "/e/pe_") || strings.HasPrefix(p, "/dl/")
	}
	re := regexp.MustCompile(`botobot\.app(/[A-Za-z0-9._/\-]*)?`)
	var files []string
	for _, g := range []string{"../cli/*.go", "../prompts/*.go", "static/*.md", "static/*.txt", "static/*.html", "static/en/*.md"} {
		m, _ := filepath.Glob(g)
		files = append(files, m...)
	}
	if len(files) < 10 {
		t.Fatalf("too few files scanned: %v", files)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			p := strings.TrimRight(m[1], ".")
			if p == "" {
				p = "/"
			}
			if !served[p] && !dynamic(p) {
				t.Errorf("%s prints botobot.app%s, which is not a route", f, p)
			}
		}
	}
}
