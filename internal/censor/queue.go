package censor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"time"
)

type Queue struct {
	mu        sync.Mutex
	config    Config
	processor Processor
	jobs      map[string]Job
	active    map[string]context.CancelFunc
	wake      chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closed    bool
	failure   error
	lock      *os.File
}

// OpenQueue owns StateDir exclusively until Close. Interrupted running jobs are retried.
func OpenQueue(c Config, p Processor) (*Queue, error) {
	c, err := normalize(c)
	if err != nil {
		return nil, err
	}
	if c.StateDir == "" {
		return nil, errors.New("censor: StateDir required")
	}
	c.StateDir, err = filepath.Abs(c.StateDir)
	if err != nil {
		return nil, err
	}
	if p == nil {
		p, err = NewEngine(c)
		if err != nil {
			return nil, err
		}
	}
	if err = os.MkdirAll(c.StateDir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(c.StateDir, "queue.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = lockQueueFile(lock); err != nil {
		lock.Close()
		return nil, fmt.Errorf("censor: state directory already in use: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	q := &Queue{config: c, processor: p, jobs: map[string]Job{}, active: map[string]context.CancelFunc{}, wake: make(chan struct{}, 1), ctx: ctx, cancel: cancel, lock: lock}
	fail := func(err error) (*Queue, error) { cancel(); lock.Close(); return nil, err }
	var data []byte
	state, err := os.Open(filepath.Join(c.StateDir, "queue.json"))
	if err != nil && !os.IsNotExist(err) {
		return fail(err)
	}
	if err == nil {
		limit := int64(c.MaxJobs) * int64(6*c.MaxTranscriptBytes+65536)
		data, err = io.ReadAll(io.LimitReader(state, limit+1))
		state.Close()
		if err != nil {
			return fail(err)
		}
		if int64(len(data)) > limit {
			return fail(errors.New("censor: queue state exceeds limit"))
		}
	}
	if len(data) > 0 {
		if err = json.Unmarshal(data, &q.jobs); err != nil {
			return fail(fmt.Errorf("censor: corrupt queue: %w", err))
		}
	}
	if q.jobs == nil {
		return fail(errors.New("censor: invalid queue state"))
	}
	if len(q.jobs) > c.MaxJobs {
		return fail(ErrQueueFull)
	}
	for id, j := range q.jobs {
		if id != j.ID || validateRequest(j.Request) != nil {
			return fail(errors.New("censor: invalid persisted job"))
		}
		switch j.Status {
		case Running:
			j.Status = Pending
			j.UpdatedAt = time.Now().UTC()
			q.jobs[id] = j
		case Pending, Succeeded, Failed, Cancelled:
		default:
			return fail(errors.New("censor: invalid persisted status"))
		}
	}
	// Exclusive ownership makes crash-leftover cleanup safe before workers start.
	if err = os.RemoveAll(filepath.Join(c.StateDir, "work")); err != nil {
		return fail(err)
	}
	leftovers, err := filepath.Glob(filepath.Join(c.StateDir, ".queue-*"))
	if err != nil {
		return fail(err)
	}
	for _, path := range leftovers {
		if err = os.Remove(path); err != nil {
			return fail(err)
		}
	}
	if err = q.persist(); err != nil {
		return fail(err)
	}
	for i := 0; i < c.Workers; i++ {
		q.wg.Add(1)
		go q.worker()
	}
	q.signal()
	return q, nil
}
func (q *Queue) persist() error {
	data, err := json.Marshal(q.jobs)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(q.config.StateDir, ".queue-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(name, filepath.Join(q.config.StateDir, "queue.json")); err != nil {
		return err
	}
	return syncQueueDirectory(q.config.StateDir)
}
func (q *Queue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}
func cloneJob(j Job) Job {
	data, _ := json.Marshal(j)
	var copy Job
	json.Unmarshal(data, &copy)
	return copy
}
func (q *Queue) usable() error {
	if q.failure != nil {
		return q.failure
	}
	if q.closed {
		return ErrClosed
	}
	return nil
}
func (q *Queue) Submit(r Request) (Job, error) {
	if err := validateRequest(r); err != nil {
		return Job{}, err
	}
	fix := func(s Source) (Source, error) {
		if s.Path != "" {
			p, err := filepath.Abs(s.Path)
			s.Path = p
			return s, err
		}
		return s, nil
	}
	var err error
	r.Source, err = fix(r.Source)
	if err != nil {
		return Job{}, err
	}
	if r.Reference != nil {
		s, err := fix(*r.Reference)
		if err != nil {
			return Job{}, err
		}
		r.Reference = &s
	}
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		return Job{}, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if err = q.usable(); err != nil {
		return Job{}, err
	}
	if len(q.jobs) >= q.config.MaxJobs {
		return Job{}, ErrQueueFull
	}
	now := time.Now().UTC()
	j := Job{ID: hex.EncodeToString(id[:]), Request: r, Status: Pending, CreatedAt: now, UpdatedAt: now}
	q.jobs[j.ID] = cloneJob(j)
	if err = q.persist(); err != nil {
		delete(q.jobs, j.ID)
		return Job{}, err
	}
	q.signal()
	return cloneJob(j), nil
}
func (q *Queue) Get(id string) (Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	j, ok := q.jobs[id]
	if !ok {
		return Job{}, ErrNotFound
	}
	return cloneJob(j), nil
}
func (q *Queue) List() []Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	jobs := make([]Job, 0, len(q.jobs))
	for _, j := range q.jobs {
		jobs = append(jobs, cloneJob(j))
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].CreatedAt.Equal(jobs[j].CreatedAt) {
			return jobs[i].ID < jobs[j].ID
		}
		return jobs[i].CreatedAt.Before(jobs[j].CreatedAt)
	})
	return jobs
}
func (q *Queue) Cancel(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.usable(); err != nil {
		return err
	}
	j, ok := q.jobs[id]
	if !ok {
		return ErrNotFound
	}
	if j.Status != Pending && j.Status != Running {
		return nil
	}
	old := j
	j.Status = Cancelled
	j.UpdatedAt = time.Now().UTC()
	j.Error = "cancelled"
	q.jobs[id] = j
	if err := q.persist(); err != nil {
		q.jobs[id] = old
		return err
	}
	if cancel := q.active[id]; cancel != nil {
		cancel()
	}
	return nil
}
func (q *Queue) Delete(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.usable(); err != nil {
		return err
	}
	j, ok := q.jobs[id]
	if !ok {
		return ErrNotFound
	}
	if j.Status == Pending || j.Status == Running {
		return errors.New("censor: cancel job before deleting")
	}
	if _, ok = q.active[id]; ok {
		return errors.New("censor: worker is still stopping")
	}
	delete(q.jobs, id)
	if err := q.persist(); err != nil {
		q.jobs[id] = j
		return err
	}
	return nil
}
func (q *Queue) Wait(ctx context.Context, id string) (Job, error) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		j, err := q.Get(id)
		if err != nil {
			return j, err
		}
		if j.Status != Pending && j.Status != Running {
			return j, nil
		}
		q.mu.Lock()
		err = q.usable()
		q.mu.Unlock()
		if err != nil {
			return j, err
		}
		select {
		case <-ctx.Done():
			return j, ctx.Err()
		case <-ticker.C:
		}
	}
}
func (q *Queue) Err() error { q.mu.Lock(); defer q.mu.Unlock(); return q.failure }
func (q *Queue) Close() error {
	q.mu.Lock()
	q.closed = true
	q.cancel()
	q.mu.Unlock()
	q.wg.Wait()
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.lock != nil {
		err := q.lock.Close()
		q.lock = nil
		if q.failure == nil {
			q.failure = err
		}
	}
	return q.failure
}
func runProcessor(ctx context.Context, p Processor, r Request) (result Result, err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("censor: processor panic: %v", v)
		}
	}()
	return p.Analyze(ctx, r)
}
func (q *Queue) worker() {
	defer q.wg.Done()
	for {
		q.mu.Lock()
		if q.closed || q.failure != nil {
			q.mu.Unlock()
			return
		}
		var chosen Job
		for _, j := range q.jobs {
			if j.Status == Pending && (chosen.ID == "" || j.CreatedAt.Before(chosen.CreatedAt)) {
				chosen = j
			}
		}
		if chosen.ID == "" {
			q.mu.Unlock()
			select {
			case <-q.ctx.Done():
				return
			case <-q.wake:
			}
			continue
		}
		old := chosen
		chosen.Status = Running
		chosen.UpdatedAt = time.Now().UTC()
		q.jobs[chosen.ID] = chosen
		if err := q.persist(); err != nil {
			q.jobs[chosen.ID] = old
			q.failure = err
			q.cancel()
			q.mu.Unlock()
			return
		}
		ctx, cancel := context.WithTimeout(q.ctx, q.config.JobTimeout)
		q.active[chosen.ID] = cancel
		q.signal()
		q.mu.Unlock()
		result, err := runProcessor(ctx, q.processor, cloneJob(chosen).Request)
		if err == nil {
			encoded, marshalErr := json.Marshal(result)
			if marshalErr != nil {
				err = fmt.Errorf("censor: invalid processor result: %w", marshalErr)
			} else if len(result.Transcript) > q.config.MaxTranscriptBytes || len(encoded) > 6*q.config.MaxTranscriptBytes+32768 {
				err = errors.New("censor: processor result exceeds limit")
			}
		}
		ctxErr := ctx.Err()
		cancel()
		q.mu.Lock()
		delete(q.active, chosen.ID)
		current := q.jobs[chosen.ID]
		if current.Status != Cancelled {
			current.UpdatedAt = time.Now().UTC()
			if q.closed {
				current.Status = Pending
				current.Error = ""
				current.Result = nil
			} else if err != nil || ctxErr != nil {
				current.Status = Failed
				if err == nil {
					err = ctxErr
				}
				current.Error = err.Error()
				if len(current.Error) > 8192 {
					current.Error = current.Error[:8192]
				}
			} else {
				current.Status = Succeeded
				current.Result = &result
				current.Error = ""
			}
			q.jobs[chosen.ID] = current
			if persistErr := q.persist(); persistErr != nil {
				q.jobs[chosen.ID] = chosen
				q.failure = persistErr
				q.cancel()
			}
		}
		q.mu.Unlock()
	}
}
