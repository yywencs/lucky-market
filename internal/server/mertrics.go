package server

import (
	"context"
	"net/http"
	"time"

	"big-market-kratos/internal/conf"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/transport"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var _ transport.Server = (*MetricsServer)(nil)

type MetricsServer struct {
	srv *http.Server
	log *log.Helper
}

func NewMetricsServer(c *conf.Monitor, logger log.Logger) *MetricsServer {
	helper := log.NewHelper(logger)
	if c == nil || !c.Enable {
		return &MetricsServer{log: helper}
	}

	mux := http.NewServeMux()
	path := c.Path
	if path == "" {
		path = "/metrics"
	}
	mux.Handle(path, promhttp.Handler())

	return &MetricsServer{
		srv: &http.Server{
			Addr:              c.Addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
		log: helper,
	}
}

func (s *MetricsServer) Start(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	s.log.Infof("metrics server listening on %s", s.srv.Addr)
	go func() {
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.log.Errorf("metrics server listen error: %v", err)
		}
	}()
	return nil
}

func (s *MetricsServer) Stop(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}
