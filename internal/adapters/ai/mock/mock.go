// Package mock provides a deterministic in-memory AIProvider for Phase 1
// tests. Same input always produces the same output, so failures bisect
// reliably and call-count assertions verify "1 segment ≤ 1 Summarize call"
// — the lazy-materialize invariant that keeps cost predictable.
package mock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"sync"

	"github.com/Leon180/workingbad/internal/domain"
	"github.com/Leon180/workingbad/internal/ports/ai"
)

// SummarizeFunc / ClassifyFunc / RelateFunc are the swappable hook
// signatures matching the AIProvider methods.
type SummarizeFunc func(ctx context.Context, changes []domain.RawChange) (title, body string, err error)
type ClassifyFunc func(ctx context.Context, content string) (domain.EntryType, float64, error)
type RelateFunc func(ctx context.Context, e domain.Entry, candidates []domain.Entry) ([]domain.Edge, error)

// Provider is a thread-safe deterministic AIProvider mock. Build via New()
// and customise via the With* methods.
type Provider struct {
	mu sync.Mutex

	summarizeFunc SummarizeFunc
	classifyFunc  ClassifyFunc
	relateFunc    RelateFunc

	summarizeCalls int
	classifyCalls  int
	relateCalls    int

	failAfterN int
}

// Compile-time interface assertion.
var _ ai.AIProvider = (*Provider)(nil)

// ErrInjected is returned by Summarize when failure injection trips.
var ErrInjected = errors.New("mock: injected failure")

// New returns a Provider seeded with deterministic defaults.
func New() *Provider {
	return &Provider{
		summarizeFunc: defaultSummarize,
		classifyFunc:  defaultClassify,
		relateFunc:    defaultRelate,
	}
}

// WithSummarizeFunc replaces the deterministic default. The caller owns
// thread-safety inside their override.
func (p *Provider) WithSummarizeFunc(f SummarizeFunc) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.summarizeFunc = f
	return p
}

// FailAfterN makes Summarize return ErrInjected after the n-th successful
// call (1-indexed). Useful for proving per-segment tx isolation.
func (p *Provider) FailAfterN(n int) *Provider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failAfterN = n
	return p
}

// Counts reports call counters in the order summarize, classify, relate.
func (p *Provider) Counts() (summarize, classify, relate int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.summarizeCalls, p.classifyCalls, p.relateCalls
}

// Summarize records the call, honours FailAfterN, and delegates to the
// (possibly overridden) summarize function.
func (p *Provider) Summarize(ctx context.Context, changes []domain.RawChange) (string, string, error) {
	p.mu.Lock()
	p.summarizeCalls++
	n := p.summarizeCalls
	failAfter := p.failAfterN
	fn := p.summarizeFunc
	p.mu.Unlock()

	if failAfter > 0 && n > failAfter {
		return "", "", ErrInjected
	}
	return fn(ctx, changes)
}

// Classify records the call and delegates.
func (p *Provider) Classify(ctx context.Context, content string) (domain.EntryType, float64, error) {
	p.mu.Lock()
	p.classifyCalls++
	fn := p.classifyFunc
	p.mu.Unlock()
	return fn(ctx, content)
}

// Relate records the call and delegates.
func (p *Provider) Relate(ctx context.Context, e domain.Entry, candidates []domain.Entry) ([]domain.Edge, error) {
	p.mu.Lock()
	p.relateCalls++
	fn := p.relateFunc
	p.mu.Unlock()
	return fn(ctx, e, candidates)
}

// defaultSummarize is a pure function of the change ids: sorted hash → stable
// title/body. Same input always yields the same output. Re-summarizing the
// same segment should therefore produce the same activity content — which
// callers can verify to prove the materialize path is deterministic.
func defaultSummarize(_ context.Context, changes []domain.RawChange) (string, string, error) {
	ids := make([]string, len(changes))
	for i, c := range changes {
		ids[i] = c.ChangeID
	}
	sort.Strings(ids)
	h := sha256.New()
	for _, id := range ids {
		h.Write([]byte(id))
		h.Write([]byte{0})
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if sum == "" {
		return "", "", errors.New("mock: empty hash")
	}
	return "mock summary " + sum[:8], "mock body for " + sum[:16], nil
}

// defaultClassify mirrors the locked Phase 1 short-circuit on the git path:
// every segment becomes an activity with confidence 1.0.
func defaultClassify(_ context.Context, _ string) (domain.EntryType, float64, error) {
	return domain.EntryTypeActivity, 1.0, nil
}

// defaultRelate returns no edges — Phase 1 mock does not propose anything.
func defaultRelate(_ context.Context, _ domain.Entry, _ []domain.Entry) ([]domain.Edge, error) {
	return nil, nil
}
