package git_test

import (
	"context"
	"io/ioutil"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/poy/onpar"
	. "github.com/poy/onpar/expect"
	. "github.com/poy/onpar/matchers"
	"github.com/poy/triple-c/internal/git"
)

type TW struct {
	*testing.T
	spyRepo       *spyRepo
	spySHATracker *spySHATracker
	shas          []string
	mu            *sync.Mutex
}

func (t *TW) Shas() []string {
	t.mu.Lock()
	defer t.mu.Unlock()

	results := make([]string, len(t.shas))
	copy(results, t.shas)

	return results
}

func TestWatcher(t *testing.T) {
	t.Parallel()
	o := onpar.New()
	defer o.Run(t)

	o.BeforeEach(func(t *testing.T) *TW {
		return &TW{
			T:             t,
			spyRepo:       newSpyRepo(),
			spySHATracker: newSpySHATracker(),
			mu:            &sync.Mutex{},
		}
	})

	o.Spec("invokes the function with the newest sha", func(t *TW) {
		t.spyRepo.errs = []error{nil, nil, nil}
		t.spyRepo.shas = []string{"sha1", "sha1", "sha2"}
		startWatcher(t)

		Expect(t, t.Shas).To(ViaPolling(Equal([]string{"sha1", "sha2"})))

		Expect(t, t.spyRepo.Branches).To(ViaPolling(Contain("some-branch")))
	})

	o.Spec("stops watching when context is canceled", func(t *TW) {
		t.spyRepo.errs = []error{nil, nil, nil}
		t.spyRepo.shas = []string{"sha1", "sha1", "sha2"}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		startWatcherWithContext(ctx, t)

		Expect(t, t.Shas).To(Always(HaveLen(0)))
	})

	o.Spec("it registers with the SHA Tracker", func(t *TW) {
		t.spyRepo.errs = []error{nil, nil, nil}
		t.spyRepo.shas = []string{"sha1", "sha1", "sha2"}

		ctx, _ := context.WithCancel(context.Background())
		startWatcherWithContext(ctx, t)

		Expect(t, t.spySHATracker.repoName).To(Equal("some-repo"))
		Expect(t, t.spySHATracker.branch).To(Equal("some-branch"))
		Expect(t, t.spySHATracker.ctx).To(Equal(ctx))

		Expect(t, t.spySHATracker.SHAs).To(ViaPolling(And(
			Contain("sha1"),
			Contain("sha2"),
		)))
	})
}

func startWatcherWithContext(ctx context.Context, t *TW) {
	git.StartWatcher(
		ctx,
		"some-repo",
		"some-branch",
		func(sha string) {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.shas = append(t.shas, sha)
		},
		time.Millisecond,
		t.spyRepo,
		t.spySHATracker,
		log.New(ioutil.Discard, "", 0),
	)
}

func startWatcher(t *TW) {
	git.StartWatcher(
		context.Background(),
		"some-repo",
		"some-branch",
		func(sha string) {
			t.mu.Lock()
			defer t.mu.Unlock()
			t.shas = append(t.shas, sha)
		},
		time.Millisecond,
		t.spyRepo,
		t.spySHATracker,
		log.New(ioutil.Discard, "", 0),
	)
}

type spyRepo struct {
	git.Repo

	mu       sync.Mutex
	branches []string

	shas []string
	errs []error
}

func newSpyRepo() *spyRepo {
	return &spyRepo{}
}

func (s *spyRepo) SHA(branch string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.branches = append(s.branches, branch)

	if len(s.shas) != len(s.errs) {
		panic("out of sync")
	}

	if len(s.shas) == 0 {
		return "", nil
	}

	sha, e := s.shas[0], s.errs[0]
	s.shas, s.errs = s.shas[1:], s.errs[1:]

	return sha, e
}

func (s *spyRepo) Branches() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	r := make([]string, len(s.branches))
	copy(r, s.branches)
	return r
}

type spySHATracker struct {
	mu sync.Mutex

	ctx      context.Context
	repoName string
	branch   string
	shas     []string
}

func newSpySHATracker() *spySHATracker {
	return &spySHATracker{}
}

func (s *spySHATracker) Register(ctx context.Context, repoName, branch string) func(SHA string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctx = ctx
	s.repoName = repoName
	s.branch = branch

	return func(SHA string) {
		s.mu.Lock()
		defer s.mu.Unlock()

		s.shas = append(s.shas, SHA)
	}
}

func (s *spySHATracker) SHAs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	results := make([]string, len(s.shas))
	copy(results, s.shas)
	return results
}
