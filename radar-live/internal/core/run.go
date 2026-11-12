package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
)

// Run starts start hooks, module Starters and the HTTP servers, then blocks until ctx is cancelled
// or a server fails. Shutdown stops the servers, then Stoppers in reverse start order, then stop
// hooks, then closes the stores. Everything started is stopped even when a later step fails.
func (h *Host) Run(ctx context.Context) error {
	if err := h.Hooks.RunStart(ctx); err != nil {
		return errors.Join(err, h.Stores.CloseAll())
	}
	started, err := h.startModules(ctx)
	if err != nil {
		return errors.Join(err, h.teardown(ctx, started))
	}
	if err := h.checkKeys(); err != nil {
		return errors.Join(err, h.teardown(ctx, started))
	}
	servers, serveErr, err := h.listen()
	if err != nil {
		return errors.Join(err, h.teardown(ctx, started))
	}
	select {
	case <-ctx.Done():
	case err = <-serveErr:
	}
	return errors.Join(err, h.shutdownServers(ctx, servers), h.teardown(ctx, started))
}

// startModules starts every module in order and returns those that started.
func (h *Host) startModules(ctx context.Context) ([]Module, error) {
	var started []Module
	for _, m := range h.modules {
		if s, ok := m.(Starter); ok {
			if err := s.Start(ctx); err != nil {
				return started, fmt.Errorf("start module %q: %w", m.Name(), err)
			}
		}
		started = append(started, m)
	}
	return started, nil
}

// endpoint is one bound listener with its server.
type endpoint struct {
	class string
	ln    net.Listener
	srv   *http.Server
}

// listen binds the public address and every enabled extra class, and serves them in the background.
func (h *Host) listen() ([]*endpoint, <-chan error, error) {
	classes := append([]listenerClass{{class: ClassPublic, key: SettingListen}}, h.classes...)
	var eps []*endpoint
	for _, c := range classes {
		addr := h.Config.String(c.key)
		if addr == "" && c.class != ClassPublic {
			h.Log.Info("listener disabled", "class", c.class, "setting", c.key)
			continue
		}
		ep, err := h.bind(c.class, addr)
		if err != nil {
			return nil, nil, errors.Join(err, h.shutdownServers(context.Background(), eps))
		}
		eps = append(eps, ep)
	}
	serveErr := make(chan error, len(eps))
	for _, ep := range eps {
		go serveEndpoint(ep, serveErr)
	}
	h.Log.Info("listening", "addr", h.Addr(), "modules", h.ModuleNames())
	close(h.ready)
	return eps, serveErr, nil
}

// bind opens one listener, applies the Server hooks and records its address.
func (h *Host) bind(class, addr string) (*endpoint, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen on %q (%s): %w", addr, class, err)
	}
	ln = h.Server.wrapListener(class, ln)
	handler := Middleware(h.Log, int64(h.Config.Int(SettingMaxBody)))(h.Audit.observeRequests(class, h.Server.wrapHandler(class, h.Routes.HandlerFor(class))))
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: readHeaderTimeout}
	h.Server.tune(class, srv)
	h.mu.Lock()
	h.addrs[class] = ln.Addr().String()
	h.mu.Unlock()
	h.Log.Info("bound", "class", class, "addr", ln.Addr().String())
	return &endpoint{class: class, ln: ln, srv: srv}, nil
}

func serveEndpoint(ep *endpoint, serveErr chan<- error) {
	if err := ep.srv.Serve(ep.ln); !errors.Is(err, http.ErrServerClosed) {
		serveErr <- fmt.Errorf("serve %s: %w", ep.class, err)
	}
}

func (h *Host) shutdownContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), h.Config.Duration(SettingShutdownTimeout))
}

func (h *Host) shutdownServers(ctx context.Context, eps []*endpoint) error {
	stopCtx, cancel := h.shutdownContext(ctx)
	defer cancel()
	var errs []error
	for _, ep := range eps {
		if err := ep.srv.Shutdown(stopCtx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown %s server: %w", ep.class, err))
		}
		_ = ep.ln.Close()
	}
	return errors.Join(errs...)
}

// teardown stops started modules in reverse order, runs stop hooks and closes the stores.
func (h *Host) teardown(ctx context.Context, started []Module) error {
	stopCtx, cancel := h.shutdownContext(ctx)
	defer cancel()
	var errs []error
	for i := len(started) - 1; i >= 0; i-- {
		if s, ok := started[i].(Stopper); ok {
			if err := s.Stop(stopCtx); err != nil {
				errs = append(errs, fmt.Errorf("stop module %q: %w", started[i].Name(), err))
			}
		}
	}
	errs = append(errs, h.Hooks.RunStop(stopCtx), h.Stores.CloseAll())
	return errors.Join(errs...)
}
