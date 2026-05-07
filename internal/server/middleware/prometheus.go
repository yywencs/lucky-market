package middleware

import (
	"context"
	"time"

	"big-market-kratos/internal/metrics"

	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/transport"
)

func MetricsMiddleware() middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req interface{}) (reply interface{}, err error) {
			start := time.Now()

			tr, ok := transport.FromServerContext(ctx)

			reply, err = handler(ctx, req)

			code := "ok"
			if err != nil {
				code = "error"
			}

			op := "unknown"
			method := "unknown"
			if ok && tr != nil {
				op = tr.Operation()
				method = tr.Kind().String()
			}

			metrics.HTTPRequestsTotal.WithLabelValues(method, op, code).Inc()
			metrics.HTTPRequestDuration.WithLabelValues(method, op).Observe(time.Since(start).Seconds())
			return
		}
	}
}
