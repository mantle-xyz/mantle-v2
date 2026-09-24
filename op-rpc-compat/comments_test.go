package main

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"testing"
	"unicode"
)

func TestGoCommentsUseEnglish(t *testing.T) {
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		for _, group := range file.Comments {
			for _, r := range group.Text() {
				if unicode.Is(unicode.Han, r) {
					t.Errorf("%s comment contains Han character %q", path, r)
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
