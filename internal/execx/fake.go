package execx

import (
	"context"
	"sync"
)

// Response is what a Fake returns for one matched call.
type Response struct {
	Result Result
	Err    error
	// Do, when non-nil, runs before the response is returned. It can
	// inspect the call and mutate shared test state (e.g. write files the
	// real command would have written).
	Do func(c Cmd)
}

type stubRule struct {
	match func(Cmd) bool
	resp  Response
}

// Fake is a scriptable, thread-safe Runner for tests. Rules are evaluated in
// registration order; the first match wins, otherwise Default is returned.
type Fake struct {
	mu      sync.Mutex
	rules   []stubRule
	calls   []Cmd
	Default Response
}

// NewFake returns an empty Fake whose default response is a zero exit.
func NewFake() *Fake {
	return &Fake{}
}

// Stub registers a rule: calls matching match receive resp.
func (f *Fake) Stub(match func(Cmd) bool, resp Response) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, stubRule{match, resp})
}

// Run implements Runner.
func (f *Fake) Run(ctx context.Context, c Cmd) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	resp := f.Default
	for _, r := range f.rules {
		if r.match(c) {
			resp = r.resp
			break
		}
	}
	f.mu.Unlock()
	if resp.Do != nil {
		resp.Do(c)
	}
	return resp.Result, resp.Err
}

// Calls returns a copy of every Cmd received so far, in order.
func (f *Fake) Calls() []Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Cmd(nil), f.calls...)
}
