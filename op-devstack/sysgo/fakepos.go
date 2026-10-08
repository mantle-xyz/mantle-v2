package sysgo

import (
	"sync"

	"github.com/ethereum-optimism/optimism/op-devstack/devtest"
	"github.com/ethereum-optimism/optimism/op-e2e/e2eutils/geth"
)

type FakePoS struct {
	mu      sync.Mutex
	p       devtest.P
	fakepos *geth.FakePoS
	started bool
}

func (f *FakePoS) Start() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.started {
		return
	}
	f.p.Require().NoError(f.fakepos.Start(), "fakePoS failed to start")
	f.started = true
}

func (f *FakePoS) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.started {
		return
	}
	f.p.Require().NoError(f.fakepos.Stop(), "fakePoS failed to stop")
	f.started = false
}
