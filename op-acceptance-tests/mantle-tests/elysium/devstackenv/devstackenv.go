// Package devstackenv points the elysium suite's sysgo systems at an Amsterdam-capable L1.
//
// Every elysium case is about what a Mantle L2 does while consuming a Glamsterdam L1, so the
// L1 EL has to be a real geth that implements EIP-7928/EIP-7843 — the in-process op-geth
// cannot serve that role. It is not merely a weaker substitute: op-geth rejects its own block 1
// with "header is missing block access list hash" once Amsterdam is active, so a system that
// falls back to it never produces an L1 at all.
//
// This lived as a per-package helpers.go copied into 26 packages verbatim. One copy is enough,
// and it matters beyond line count: the fallback behaviour below is a policy decision, and a
// policy that exists in 26 places is one that gets changed in 1.
package devstackenv

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ethereum-optimism/optimism/op-devstack/sysgo"
)

// Configure locates a mise-installed geth and selects it as the L1 EL, returning a function
// that restores the previous environment. Call it from TestMain and defer the result.
//
// It is a no-op when the runner has already chosen an L1 EL through either
// sysgo.DevstackL1ELKindEnvVar or sysgo.GethExecPathEnvVar, so an explicit choice — including
// a deliberate "use the in-process op-geth" for a test that wants it — always wins.
//
// When neither is set and mise cannot produce a geth, this PANICS rather than returning
// quietly. The alternative, which this code used to do, was to print a line to stdout and let
// the system come up on the in-process op-geth: the suite would then fail deep inside block
// production with an error naming a missing block-access-list hash, which says nothing about
// the actual cause. Failing here names it.
func Configure() func() {
	if _, ok := os.LookupEnv(sysgo.DevstackL1ELKindEnvVar); ok {
		return func() {}
	}
	if _, ok := os.LookupEnv(sysgo.GethExecPathEnvVar); ok {
		return func() {}
	}

	cmd := exec.Command("mise", "which", "geth")
	buf := bytes.NewBuffer([]byte{})
	cmd.Stdout = buf
	if err := cmd.Run(); err != nil {
		panic(fmt.Sprintf(
			"elysium requires an Amsterdam-capable L1 geth and `mise which geth` failed: %v\n"+
				"Install it (mise install geth), or point the suite at one explicitly with %s=<path> "+
				"and %s=geth. Continuing on the in-process op-geth is not an option: it cannot produce "+
				"a valid post-Amsterdam L1 block.",
			err, sysgo.GethExecPathEnvVar, sysgo.DevstackL1ELKindEnvVar))
	}
	execPath := strings.TrimSpace(buf.String())
	if execPath == "" {
		panic(fmt.Sprintf(
			"`mise which geth` succeeded but produced no path; set %s=<path> and %s=geth explicitly",
			sysgo.GethExecPathEnvVar, sysgo.DevstackL1ELKindEnvVar))
	}
	fmt.Println("elysium: using mise-installed geth as the L1 EL:", execPath)

	_ = os.Setenv(sysgo.GethExecPathEnvVar, execPath)
	_ = os.Setenv(sysgo.DevstackL1ELKindEnvVar, "geth")
	return func() {
		_ = os.Unsetenv(sysgo.GethExecPathEnvVar)
		_ = os.Unsetenv(sysgo.DevstackL1ELKindEnvVar)
	}
}
