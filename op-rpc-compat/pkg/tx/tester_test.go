package tx

import (
	"errors"
	"testing"
)

func TestIsAlreadyKnownError(t *testing.T) {
	if !isAlreadyKnownError(errors.New("RPC error: code=-32000, message=already known")) {
		t.Fatal("expected already-known RPC error to be recognized")
	}
	if isAlreadyKnownError(errors.New("RPC error: code=-32000, message=nonce too low")) {
		t.Fatal("nonce-too-low error must not be recognized as already known")
	}
	if isAlreadyKnownError(nil) {
		t.Fatal("nil error must not be recognized as already known")
	}
}
