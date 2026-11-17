package testcases

import (
	"encoding/json"
	"io/fs"
	"strconv"
	"testing"
	"unicode"
)

func TestDefaultCorpusExcludesLegacyKnownDiffs(t *testing.T) {
	for _, name := range []string{"eth_basic.json"} {
		data, err := fs.ReadFile(FS, name)
		if err != nil {
			t.Errorf("read %s: %v", name, err)
			continue
		}
		if !json.Valid(data) {
			t.Errorf("%s is not valid JSON", name)
		}
	}
	if _, err := fs.ReadFile(FS, "known_diffs.json"); err == nil {
		t.Fatal("legacy known differences must not be embedded")
	}
}

func TestEmbeddedMetadataUsesEnglish(t *testing.T) {
	files, err := fs.Glob(FS, "*.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		data, err := fs.ReadFile(FS, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var value interface{}
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		if path := firstChineseMetadata(value, ""); path != "" {
			t.Errorf("%s has Chinese metadata at %s", name, path)
		}
	}
}

func firstChineseMetadata(value interface{}, path string) string {
	switch value := value.(type) {
	case map[string]interface{}:
		for key, child := range value {
			childPath := path + "." + key
			if key == "description" || key == "reason" || key == "_note" {
				if text, ok := child.(string); ok {
					for _, r := range text {
						if unicode.Is(unicode.Han, r) {
							return childPath
						}
					}
				}
			}
			if found := firstChineseMetadata(child, childPath); found != "" {
				return found
			}
		}
	case []interface{}:
		for i, child := range value {
			if found := firstChineseMetadata(child, path+"["+strconv.Itoa(i)+"]"); found != "" {
				return found
			}
		}
	}
	return ""
}
