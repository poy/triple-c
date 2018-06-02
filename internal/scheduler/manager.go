package scheduler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/apoydence/triple-c/internal/git"
)

type Manager struct {
	log             *log.Logger
	m               Metrics
	successfulTasks func(delta uint64)
	failedTasks     func(delta uint64)
	failedRepos     func(delta uint64)
	dedupedTasks    func(delta uint64)
	appGuid         string
	branch          string
	ps              ParameterStore

	taskCreator TaskCreator
	shaTracker  git.SHATracker

	startWatcher GitWatcher
	repoRegistry RepoRegistry

	mu   sync.Mutex
	ctxs map[encodedTask]func()
}

type GitWatcher func(
	ctx context.Context,
	repoName string,
	branch string,
	commit func(SHA string),
	interval time.Duration,
	shaFetcher git.SHAFetcher,
	shaTracker git.SHATracker,
	m git.Metrics,
	log *log.Logger,
)

type TaskCreator interface {
	CreateTask(
		command string,
		name string,
		appGuid string,
	) error

	ListTasks(appGuid string) ([]string, error)
}

type ParameterStore func(key string) (string, bool)

type Metrics interface {
	NewCounter(name string) func(delta uint64)
}

type RepoRegistry interface {
	FetchRepo(path string) (*git.Repo, error)
}

func NewManager(
	ctx context.Context,
	appGuid string,
	branch string,
	tc TaskCreator,
	w GitWatcher,
	repoRegistry RepoRegistry,
	ps ParameterStore,
	shaTracker git.SHATracker,
	m Metrics,
	log *log.Logger,
) *Manager {

	successfulTasks := m.NewCounter("SuccessfulTasks")
	failedTasks := m.NewCounter("FailedTasks")
	dedupedTasks := m.NewCounter("DedupedTasks")
	failedRepos := m.NewCounter("FailedRepos")

	return &Manager{
		log:          log,
		startWatcher: w,
		repoRegistry: repoRegistry,
		appGuid:      appGuid,
		branch:       branch,
		m:            m,
		ps:           ps,

		shaTracker:  shaTracker,
		taskCreator: tc,

		successfulTasks: successfulTasks,
		failedTasks:     failedTasks,
		failedRepos:     failedRepos,
		dedupedTasks:    dedupedTasks,

		ctxs: make(map[encodedTask]func()),
	}
}

func (m *Manager) Add(t MetaPlan) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Printf("Adding task: %+v", t)
	ctx, cancel := context.WithCancel(context.Background())
	m.ctxs[encodePlan(t)] = cancel

	var taskLock sync.Mutex

	for _, repoPath := range t.RepoPaths {
		repo, err := m.repoRegistry.FetchRepo(repoPath)
		if err != nil {
			m.log.Printf("failed to fetch repo %s: %s", repoPath, err)
			m.failedRepos(1)
			return
		}

		m.startWatcher(
			ctx,
			repoPath,
			m.branch,
			func(SHA string) {
				m.startPlanForSHA(SHA, t, &taskLock)
			},
			time.Minute,
			repo,
			m.shaTracker,
			m.m,
			m.log,
		)
	}
}

func (m *Manager) startPlanForSHA(SHA string, t MetaPlan, taskLock *sync.Mutex) {
	if !m.checkAndRemove(t, t.DoOnce) {
		return
	}

	dupe, err := m.duplicate(m.branch, SHA)
	if err != nil {
		m.log.Printf("failed deduping tasks: %s", err)
		return
	}

	if dupe {
		m.log.Printf("skipping task for %s on branch %s", SHA, m.branch)
		m.dedupedTasks(1)
		return
	}

	taskLock.Lock()
	defer taskLock.Unlock()

	for taskIndex, task := range t.Tasks {
		if task.BranchGuard != "" && task.BranchGuard != m.branch {
			m.log.Printf("skipping task for %s on branch %s (BranchGuard %s)", SHA, m.branch, task.BranchGuard)
			continue
		}

		if !m.startTaskForSHA(SHA, task, t, taskIndex) {
			return
		}
	}
}

func (m *Manager) startTaskForSHA(SHA string, task Task, t MetaPlan, taskIndex int) bool {
	m.log.Printf("starting task for %s on branch %s", SHA, m.branch)
	defer m.log.Printf("done with task for %s on branch %s", SHA, m.branch)

	name, err := json.Marshal(struct {
		SHA       string `json:"sha"`
		Branch    string `json:"branch"`
		TaskIndex int    `json:"task_index"`
	}{
		SHA:       SHA,
		Branch:    m.branch,
		TaskIndex: taskIndex,
	})
	if err != nil {
		m.log.Printf("failed to marshal task name: %s", err)
		return false
	}

	err = m.taskCreator.CreateTask(
		m.fetchRepo(t, task, m.branch, m.ps),
		base64.StdEncoding.EncodeToString(name),
		m.appGuid,
	)
	if err != nil {
		m.log.Printf("task for %s failed: %s", SHA, err)
		m.failedTasks(1)
		return false
	}

	m.log.Printf("task for %s on branch %s succeeded", SHA, m.branch)
	m.successfulTasks(1)
	return true
}

func (m *Manager) duplicate(branch, SHA string) (bool, error) {
	tasks, err := m.taskCreator.ListTasks(m.appGuid)
	if err != nil {
		return false, err
	}

	for _, t := range tasks {
		data, err := base64.StdEncoding.DecodeString(t)
		if err != nil {
			continue
		}

		var taskMeta struct {
			SHA    string `json:"sha"`
			Branch string `json:"branch"`
		}
		if err := json.Unmarshal(data, &taskMeta); err != nil {
			continue
		}

		if taskMeta.Branch == branch && taskMeta.SHA == SHA {
			return true, nil
		}
	}

	return false, nil
}

func (m *Manager) Remove(t MetaPlan) {
	m.checkAndRemove(t, true)
}

func (m *Manager) checkAndRemove(t MetaPlan, remove bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	cancel, ok := m.ctxs[encodePlan(t)]
	if !ok {
		return false
	}

	if !remove {
		return true
	}

	delete(m.ctxs, encodePlan(t))
	cancel()

	return true
}

type encodedTask string

func encodePlan(p MetaPlan) encodedTask {
	parameters := []string{
		p.Name,
	}

	for k, v := range p.RepoPaths {
		parameters = append(parameters, k, v)
	}

	for _, t := range p.Tasks {
		parameters = append(parameters, t.Command, t.Name)
		for k, v := range t.Parameters {
			parameters = append(parameters, fmt.Sprintf("%s=%s", k, v))
		}
	}
	sort.Strings(parameters)

	return encodedTask(strings.Join(parameters, ","))
}

// fetchRepo adds the cloning of a repo to the given command
func (m *Manager) fetchRepo(p MetaPlan, t Task, branch string, ps ParameterStore) string {
	var parameters string
	for k, v := range t.Parameters {
		if !strings.HasPrefix(v, "((") || !strings.HasSuffix(v, "))") {
			parameters = fmt.Sprintf("%sexport %s=%s\n", parameters, k, v)
			continue
		}

		if v, ok := ps(v[2 : len(v)-2]); ok {
			parameters = fmt.Sprintf("%sexport %s=%s\n", parameters, k, v)
			continue
		}
	}

	var clones string
	for _, repoPath := range p.RepoPaths {
		clones = fmt.Sprintf(`
%s
rm -rf %s
git clone %s

pushd %s
  # If checking out fails, its fine. Move forward with the default branch.
  set +e
  git checkout %s
  set -e

  git submodule update --init --recursive
popd

set +x
`,
			clones,
			path.Base(repoPath),
			repoPath,
			path.Base(repoPath),
			branch,
		)
	}

	return fmt.Sprintf(`#!/bin/bash
set -ex

%s

# Parameters
%s

%s
	`,
		clones,
		parameters,
		t.Command,
	)
}
