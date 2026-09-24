package testcases

import (
	"encoding/json"
	"io/fs"
	"testing"
)

func TestDefaultCorpusIncludesCasesAndKnownDiffs(t *testing.T) {
	for _, name := range []string{"eth_basic.json", "known_diffs.json"} {
		data, err := fs.ReadFile(FS, name)
		if err != nil {
			t.Errorf("read %s: %v", name, err)
			continue
		}
		if !json.Valid(data) {
			t.Errorf("%s is not valid JSON", name)
		}
	}
}
