package resources

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
)

type panicSampler struct{ fakeSys }

func (*panicSampler) Machine() (sysinfo.Machine, error) { panic("sampler died") }
func TestGovernorPanicAndStaleObservation(t *testing.T) {
	m := New(Options{Sys: &panicSampler{}})
	m.Attach(&fakeFleet{})
	m.lastCompleted.Store(time.Now().UnixNano())
	m.staleAfter.Store(int64(time.Second))
	if !m.Fresh() {
		t.Fatal("fresh sample missing")
	}
	done := make(chan struct{})
	go func() { defer close(done); m.Run(context.Background()) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("panic not contained")
	}
	if m.Fresh() {
		t.Fatal("dead governor reported healthy")
	}
	m.lastCompleted.Store(time.Now().Add(-2 * time.Second).UnixNano())
	if m.Fresh() {
		t.Fatal("stale governor reported healthy")
	}
	// Fresh must not depend on the governor's own mutex.
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Fresh() {
		t.Fatal("stale sampler reported healthy")
	}
}

type failingMachine struct{ *fakeSys }

func (s failingMachine) Machine() (sysinfo.Machine, error) {
	v, _ := s.fakeSys.Machine()
	return v, errors.New("persistent sampler failure")
}
func TestPersistentSamplerErrorDoesNotFlapBrakes(t *testing.T) {
	r := newRig(t, config.Resources{MemoryCapMB: 1})
	r.m.sys = failingMachine{r.sys}
	for n := 0; n < 8; n++ {
		r.step()
		r.m.restoreOnDegradation()
	}
	if r.m.Fresh() || len(r.fleet.brakes) != 0 || len(r.fleet.unbrakes) != 0 {
		t.Fatal("sampler caused brake/resume loop", r.fleet.brakes, r.fleet.unbrakes)
	}
}

func TestDeadGovernorRestoresOnlyResourcePauses(t *testing.T) {
	f := &fakeFleet{agents: []model.Agent{
		{ID: "a", Name: "a", State: model.StatePaused, PausedReason: BrakeDisk},
		{ID: "b", Name: "b", State: model.StatePaused, PausedReason: "other"},
	}}
	m := New(Options{})
	m.Attach(f)
	m.led.Paused["a"] = []sysinfo.Proc{{PID: 123, Start: 1}}
	m.diskTurns["a"] = time.Now()
	m.mu.Lock() // even a stuck governor lock must not block recovery
	m.restoreOnDegradation()
	m.mu.Unlock()
	m.restoreOnDegradation() // reconcile after the previously wedged lock is free
	if len(f.unbrakes) != 1 || f.unbrakes[0] != "a:"+BrakeDisk {
		t.Fatal(f.unbrakes)
	}
	if len(m.led.Paused) != 0 || len(m.diskTurns) != 0 {
		t.Fatal("stale pause bookkeeping survived")
	}
}

type diskErr struct {
	*fakeSys
	err error
}

func (s diskErr) Disks() ([]sysinfo.Disk, error) { return nil, s.err }

func TestUnsupportedDiskIOKeepsGovernorHealthy(t *testing.T) {
	r := newRig(t, config.Resources{MemoryCapMB: 600, DiskQueue: 8, DiskBusySecs: 1, DiskAgentMBs: 1})
	r.m.sys = diskErr{r.sys, sysinfo.ErrUnsupported}
	r.step()
	r.sys.add(300, 0, 500*MB, 900*MB)
	r.step()
	if !r.m.Fresh() {
		t.Fatal("unmeasurable disk I/O degraded the governor; background jobs would never be admitted")
	}
	if len(r.fleet.brakes) != 1 || r.fleet.brakes[0] != "w2:"+BrakeMemory {
		t.Fatalf("memory brake dropped: %v", r.fleet.brakes)
	}
	if s := r.m.View().Sample.Machine; s.DiskQueue != -1 || s.DiskWriteRate != -1 || s.Saturated {
		t.Fatalf("disk I/O must read as not measured: %+v", s)
	}
}

func TestDiskReadErrorStaysDegraded(t *testing.T) {
	r := newRig(t, config.Resources{MemoryCapMB: 600})
	r.m.sys = diskErr{r.sys, errors.New("diskstats unreadable")}
	r.step()
	r.sys.add(300, 0, 0, 900*MB)
	r.step()
	if r.m.Fresh() || len(r.fleet.brakes) != 0 {
		t.Fatalf("a real disk read error must stay fail-safe: fresh=%v brakes=%v", r.m.Fresh(), r.fleet.brakes)
	}
}
