// Package rde provides a ServiceLifecycleSurface for devnets managed by rde
// (https://github.com/mantle-xyz/rde-v4), a binary+profile devnet orchestrator.
//
// The descriptor for an rde devnet is a plain file (hand-authored or generated),
// so it is loaded through the normal file backend; only the *control* surface is
// rde-specific. Select it by setting DEVNET_ENV_CTRL=rde alongside DEVNET_ENV_URL,
// which makes the loader take the control surface from this package while still
// reading the descriptor from the file.
//
// Service names are passed through verbatim, so the descriptor's service names
// must match rde's service names (e.g. "op-node", "op-geth-rpc1", "geth0").
package rde

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"

	"github.com/ethereum-optimism/optimism/devnet-sdk/controller/surface"
	"github.com/ethereum-optimism/optimism/devnet-sdk/descriptors"
)

const (
	// EnvDir points at the rde checkout (where Taskfile.yaml lives).
	EnvDir = "RDE_DIR"
	// EnvProfile is the rde profile the devnet is running under.
	EnvProfile = "RDE_PROFILE"
	// EnvCmd overrides the launcher binary (default "task"). rde's own convention
	// is to go through `task` so that mise injects the tool PATH for spawned services.
	EnvCmd = "RDE_CMD"
)

type RdeControllerSurface struct {
	mtx     sync.Mutex
	dir     string
	profile string
	cmd     string
	env     *descriptors.DevnetEnvironment
}

func NewRdeControllerSurface(env *descriptors.DevnetEnvironment) (*RdeControllerSurface, error) {
	dir := os.Getenv(EnvDir)
	if dir == "" {
		return nil, fmt.Errorf("%s must be set to the rde checkout directory", EnvDir)
	}
	profile := os.Getenv(EnvProfile)
	if profile == "" {
		return nil, fmt.Errorf("%s must be set to the running rde profile", EnvProfile)
	}
	cmd := os.Getenv(EnvCmd)
	if cmd == "" {
		cmd = "task"
	}
	return &RdeControllerSurface{dir: dir, profile: profile, cmd: cmd, env: env}, nil
}

// run invokes `task <action> -- <service> --profile <profile>` in the rde checkout.
// Both actions are idempotent on the rde side, so repeated calls are safe.
func (s *RdeControllerSurface) run(ctx context.Context, action, serviceName string) error {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	c := exec.CommandContext(ctx, s.cmd, action, "--", serviceName, "--profile", s.profile)
	c.Dir = s.dir
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("rde %s %q (profile %s) failed: %w: %s",
			action, serviceName, s.profile, err, string(out))
	}
	return nil
}

func (s *RdeControllerSurface) StartService(ctx context.Context, serviceName string) error {
	return s.run(ctx, "start", serviceName)
}

func (s *RdeControllerSurface) StopService(ctx context.Context, serviceName string) error {
	return s.run(ctx, "stop", serviceName)
}

var _ surface.ServiceLifecycleSurface = (*RdeControllerSurface)(nil)
