// Package agentbox adapts the official SDK to the executor's narrow I/O contract.
package agentbox

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"maps"
	"sync"
	"time"

	sdk "github.com/abox-dev/sdk/packages/go-sdk"
	"github.com/orpheus-agents/orpheus/internal/harness"
)

type Platform struct {
	client *sdk.Client
	owner  map[string]string
}

func (p *Platform) WithOwner(owner map[string]string) harness.Platform {
	return &Platform{client: p.client, owner: maps.Clone(owner)}
}
func (p *Platform) matches(id string, info *sdk.SandboxInfo, expected map[string]string) bool {
	if len(expected) == 0 {
		return true
	}
	if info == nil || info.SandboxID != id || info.Metadata == nil {
		return false
	}
	for k, v := range expected {
		if (*info.Metadata)[k] != v {
			return false
		}
	}
	return true
}
func (p *Platform) verify(ctx context.Context, id string, expected map[string]string) error {
	if len(expected) == 0 {
		return nil
	}
	info, err := p.client.Sandboxes.Info(ctx, id)
	if err != nil {
		return classify(err)
	}
	if !p.matches(id, info, expected) {
		return errors.New("sandbox owner mismatch")
	}
	return nil
}
func (p *Platform) labels(metadata map[string]string) map[string]string {
	if len(p.owner) == 0 {
		return metadata
	}
	result := maps.Clone(metadata)
	if result == nil {
		result = map[string]string{}
	}
	maps.Copy(result, p.owner)
	return result
}

func New() (*Platform, error) {
	c, err := sdk.NewClient()
	if err != nil {
		return nil, err
	}
	return &Platform{client: c}, nil
}
func (p *Platform) Create(ctx context.Context, template string, timeout time.Duration, metadata map[string]string) (harness.Sandbox, error) {
	metadata = p.labels(metadata)
	s, err := p.client.Sandboxes.Create(ctx, &sdk.CreateSandboxOptions{Template: template, Timeout: timeout, Metadata: metadata, AutoPause: new(true), AutoPauseMemory: new(true), AutoResume: new(false)})
	if err != nil {
		return nil, classify(err)
	}
	if len(p.owner) > 0 {
		if err := p.verify(ctx, s.ID, metadata); err != nil {
			return nil, err
		}
	}
	return &sandbox{s: s, platform: p}, nil
}
func (p *Platform) Connect(ctx context.Context, id string, timeout time.Duration) (harness.Sandbox, error) {
	if err := p.verify(ctx, id, p.owner); err != nil {
		return nil, err
	}
	s, err := p.client.Sandboxes.Connect(ctx, id, &sdk.ConnectSandboxOptions{Timeout: timeout})
	if err != nil {
		return nil, classify(err)
	}
	return &sandbox{s: s, platform: p}, nil
}
func (p *Platform) Delete(ctx context.Context, id string) error {
	if err := p.verify(ctx, id, p.owner); err != nil {
		return err
	}
	_, err := p.client.Sandboxes.Kill(ctx, id)
	return classify(err)
}
func (p *Platform) Info(ctx context.Context, id string) (string, error) {
	s, err := p.client.Sandboxes.Info(ctx, id)
	if err != nil {
		return "", classify(err)
	}
	if !p.matches(id, s, p.owner) {
		return "", errors.New("sandbox owner mismatch")
	}
	return string(s.State), nil
}
func (p *Platform) Find(ctx context.Context, metadata map[string]string) ([]string, error) {
	metadata = p.labels(metadata)
	out := []string{}
	next := ""
	for {
		page, err := p.client.Sandboxes.List(ctx, &sdk.ListSandboxOptions{Metadata: metadata, States: []sdk.SandboxState{sdk.SandboxRunning, sdk.SandboxPaused}, NextToken: next})
		if err != nil {
			return nil, classify(err)
		}
		for _, s := range page.Items {
			if len(p.owner) > 0 {
				if err := p.verify(ctx, s.SandboxID, metadata); err != nil {
					return nil, err
				}
			}
			out = append(out, s.SandboxID)
		}
		if page.NextToken == "" {
			return out, nil
		}
		next = page.NextToken
	}
}

type sandbox struct {
	s        *sdk.Sandbox
	platform *Platform
}

const sandboxUser = "user"

func (s *sandbox) ID() string { return s.s.ID }
func (s *sandbox) Pause(ctx context.Context) error {
	if err := s.platform.verify(ctx, s.ID(), s.platform.owner); err != nil {
		return err
	}
	return classify(s.s.Pause(ctx, &sdk.PauseOptions{Memory: new(true)}))
}
func (s *sandbox) SetTimeout(ctx context.Context, d time.Duration) error {
	return classify(s.s.SetTimeout(ctx, d))
}
func (s *sandbox) Processes(ctx context.Context) ([]harness.Process, error) {
	ps, err := s.s.Commands.List(ctx)
	if err != nil {
		return nil, classify(err)
	}
	out := make([]harness.Process, 0, len(ps))
	for _, p := range ps {
		out = append(out, harness.Process{PID: int(p.PID), Env: p.Env})
	}
	return out, nil
}
func (s *sandbox) Kill(ctx context.Context, pid int) error {
	return classify(s.s.Commands.Kill(ctx, uint32(pid), ""))
}
func (s *sandbox) Run(ctx context.Context, command string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, err := s.s.Commands.Run(ctx, "/bin/sh", &sdk.CommandOptions{User: sandboxUser, Args: []string{"-c", command}})
	return r.Stdout, classify(err)
}
func (s *sandbox) Start(ctx context.Context, command string, env map[string]string, cwd string) (harness.Stream, int, error) {
	return openStream(ctx, 60*time.Second, func(ctx context.Context) (*sdk.CommandHandle, error) {
		return s.s.Commands.Start(ctx, "/bin/sh", &sdk.CommandOptions{User: sandboxUser, Args: []string{"-c", command}, Env: env, Cwd: cwd, Stdin: true, Streaming: &sdk.CommandStreamingOptions{StdoutChannel: true}})
	})
}
func (s *sandbox) Attach(ctx context.Context, pid int) (harness.Stream, error) {
	h, _, err := openStream(ctx, 60*time.Second, func(ctx context.Context) (*sdk.CommandHandle, error) {
		return s.s.Commands.ConnectWithOptions(ctx, uint32(pid), "", &sdk.CommandConnectOptions{Streaming: &sdk.CommandStreamingOptions{StdoutChannel: true}})
	})
	return h, err
}

// Bound connection setup and PID acknowledgement, without imposing a deadline
// on the long-lived stream. A setup timeout is uncertain and never retries Start.
func openStream(ctx context.Context, timeout time.Duration, open func(context.Context) (*sdk.CommandHandle, error)) (harness.Stream, int, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	readyCtx, readyCancel := context.WithTimeout(ctx, timeout)
	defer readyCancel()
	stop := context.AfterFunc(readyCtx, cancel)
	h, err := open(streamCtx)
	var pid uint32
	if err == nil {
		pid, err = h.PID(readyCtx)
	}
	if !stop() && err == nil {
		err = readyCtx.Err()
	}
	if err != nil {
		cancel()
		if h != nil {
			_ = h.Close()
		}
		return nil, 0, classify(err)
	}
	return &stream{h: h, cancel: cancel}, int(pid), nil
}
func (s *sandbox) Read(ctx context.Context, path string) (io.ReadCloser, error) {
	r, err := s.s.Files.Read(ctx, path, &sdk.FileOptions{User: sandboxUser})
	if _, ok := errors.AsType[*sdk.FileNotFoundError](err); ok {
		return nil, fs.ErrNotExist
	}
	return r, classify(err)
}
func (s *sandbox) Write(ctx context.Context, path string, b []byte) error {
	_, err := s.s.Files.WriteBytes(ctx, path, b, &sdk.WriteFileOptions{User: sandboxUser})
	return classify(err)
}

type stream struct {
	h      *sdk.CommandHandle
	cancel context.CancelFunc
}

func (s *stream) Stdout() <-chan []byte { return s.h.Stdout }
func (s *stream) Stderr() <-chan []byte { return s.h.Stderr }
func (s *stream) Write(ctx context.Context, b []byte) (int, error) {
	n, err := s.h.Write(ctx, b)
	return n, classify(err)
}
func (s *stream) Close() error { s.cancel(); return s.h.Close() }

type watch struct {
	h      *sdk.WatchHandle
	events chan string
	done   chan struct{}
	cancel context.CancelFunc
	once   sync.Once
}

func (s *sandbox) Watch(ctx context.Context, path string) (harness.Watch, error) {
	ctx, cancel := context.WithCancel(ctx)
	h, err := s.s.Files.Watch(ctx, path, &sdk.WatchOptions{User: sandboxUser})
	if err != nil {
		cancel()
		return nil, classify(err)
	}
	w := &watch{h: h, events: make(chan string, 1), done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(w.done)
		defer close(w.events)
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-h.Events:
				if !ok {
					return
				}
				select {
				case w.events <- e.Name:
				default:
				}
			}
		}
	}()
	return w, nil
}
func (w *watch) Events() <-chan string { return w.events }
func (w *watch) Done() <-chan struct{} { return w.done }
func (w *watch) Close() error {
	var err error
	w.once.Do(func() { w.cancel(); err = w.h.Close(); <-w.done })
	return err
}

// Preserve the HTTP status without exposing the upstream message to the journal.
type platformError struct {
	cause  error
	status int
}

func (e *platformError) Error() string   { return "AgentBox request failed" }
func (e *platformError) Unwrap() error   { return e.cause }
func (e *platformError) HTTPStatus() int { return e.status }

func classify(err error) error {
	classified := classifyError(err)
	status := 0
	if err == nil {
		return nil
	}
	if e, ok := errors.AsType[*sdk.SandboxError](err); ok {
		status = e.StatusCode
	}
	if e, ok := errors.AsType[*sdk.RateLimitError](err); ok {
		status = e.StatusCode
	}
	if e, ok := errors.AsType[*sdk.TimeoutError](err); ok {
		status = e.StatusCode
	}
	if e, ok := errors.AsType[*sdk.AuthenticationError](err); ok {
		status = e.StatusCode
	}
	if e, ok := errors.AsType[*sdk.SandboxNotFoundError](err); ok {
		status = e.StatusCode
	}
	if e, ok := errors.AsType[*sdk.TemplateError](err); ok {
		status = e.StatusCode
	}
	if status != 0 {
		return &platformError{cause: classified, status: status}
	}
	return classified
}
func classifyError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*sdk.SandboxNotFoundError](err); ok {
		return harness.ErrNotFound
	}
	if _, ok := errors.AsType[*sdk.FileNotFoundError](err); ok {
		return harness.ErrEnvironmentRejected
	}
	if _, ok := errors.AsType[*sdk.AuthenticationError](err); ok {
		return harness.ErrEnvironmentRejected
	}
	if _, ok := errors.AsType[*sdk.InvalidArgumentError](err); ok {
		return harness.ErrEnvironmentRejected
	}
	if _, ok := errors.AsType[*sdk.CommandExitError](err); ok {
		return harness.ErrEnvironmentRejected
	}
	if _, ok := errors.AsType[*sdk.NotEnoughSpaceError](err); ok {
		return harness.ErrEnvironmentRejected
	}
	if _, ok := errors.AsType[*sdk.TemplateError](err); ok {
		return harness.ErrEnvironmentRejected
	}
	if e, ok := errors.AsType[*sdk.SandboxError](err); ok && e.StatusCode >= 400 && e.StatusCode < 500 && e.StatusCode != 408 && e.StatusCode != 429 {
		return harness.ErrEnvironmentRejected
	}
	return err
}
