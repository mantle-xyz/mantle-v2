package policies

import (
	"testing"

	"github.com/ethereum-optimism/optimism/op-rpc-compat/pkg/policy"
)

func TestEmbeddedRegistryLoadsOutsideRepository(t *testing.T) {
	t.Chdir(t.TempDir())
	registry, err := policy.Load(FS, "accepted/registry.json")
	if err != nil {
		t.Fatal(err)
	}
	if registry.SchemaVersion != 2 || registry.RegistryID == "" || len(registry.Rules) != 0 {
		t.Fatalf("initial registry = %+v", registry)
	}
}
