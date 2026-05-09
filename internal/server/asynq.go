// internal/server/asynq.go
package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"big-market-kratos/internal/biz/activity"
	"big-market-kratos/internal/biz/task"
	"big-market-kratos/internal/conf"
	"big-market-kratos/internal/job" // 假设你把 Job 放在了 internal/job 目录下
	"big-market-kratos/internal/metrics"
	"big-market-kratos/pkg/logger"

	"github.com/go-kratos/kratos/v2/transport"
	"github.com/hibiken/asynq"
)

// 确保 AsynqServer 严格实现了 Kratos 的 Server 接口
var _ transport.Server = (*AsynqServer)(nil)

type AsynqServer struct {
	server    *asynq.Server
	mux       *asynq.ServeMux
	scheduler *asynq.Scheduler
	inspector *asynq.Inspector
	queues    []string
}

func NewAsynqServer(
	cfg *conf.Asynq,
	skuStockJob *job.ActivitySkuStockConsumeJob,
	stateSyncJob *job.SendAwardMessage,
	strategyAwardStockJob *job.StrategyAwardStockConsumeJob,
) *AsynqServer {
	redisOpt := asynq.RedisClientOpt{
		Addr:     fmt.Sprintf("%s:%d", cfg.Redis.Host, cfg.Redis.Port),
		DB:       int(cfg.Redis.Db),
		PoolSize: int(cfg.Redis.PoolSize),
	}

	server := asynq.NewServer(
		redisOpt,
		asynq.Config{
			Concurrency: int(cfg.Concurrency),
			Queues: map[string]int{
				activity.TaskTypeActivitySkuStockConsume: 6,
				"default":                                3,
				"low":                                    1,
			},
			ErrorHandler: asynq.ErrorHandlerFunc(func(ctx context.Context, task *asynq.Task, err error) {
				metrics.IncAsynqTask(task.Type(), asynqTaskResult(err))
				logger.Error("process task failed", "type", task.Type(), "payload", string(task.Payload()), "err", err)
			}),
		},
	)

	mux := asynq.NewServeMux()
	scheduler := asynq.NewScheduler(redisOpt, &asynq.SchedulerOpts{})
	inspector := asynq.NewInspector(redisOpt)
	queues := []string{
		activity.TaskTypeActivitySkuStockConsume,
		"default",
		"low",
	}

	mux.HandleFunc(activity.TaskTypeActivitySkuStockConsume, wrapAsynqHandler(activity.TaskTypeActivitySkuStockConsume, skuStockJob.ProcessTask))
	mux.HandleFunc(activity.TaskTypeActivityStateSync, wrapAsynqHandler(activity.TaskTypeActivityStateSync, stateSyncJob.ProcessTask))
	mux.HandleFunc(task.TaskTypeStrategyAwardStockConsume, wrapAsynqHandler(task.TaskTypeStrategyAwardStockConsume, strategyAwardStockJob.ProcessTask))

	// 注册定时任务
	if _, err := scheduler.Register("@every 5s", asynq.NewTask(activity.TaskTypeActivityStateSync, nil)); err != nil {
		logger.Error("Register scheduler failed", "err", err)
	}

	return &AsynqServer{
		server:    server,
		mux:       mux,
		scheduler: scheduler,
		inspector: inspector,
		queues:    queues,
	}
}

func (s *AsynqServer) Start(ctx context.Context) error {
	logger.Info("Asynq Server starting...")
	s.startQueueMetricsCollector(ctx)
	if err := s.scheduler.Start(); err != nil {
		return fmt.Errorf("scheduler start failed: %w", err)
	}

	if err := s.server.Start(s.mux); err != nil {
		return fmt.Errorf("asynq server start failed: %w", err)
	}
	return nil
}

// 适配 Kratos 的 Stop(ctx)
func (s *AsynqServer) Stop(ctx context.Context) error {
	logger.Info("Asynq Server stopping...")
	s.scheduler.Shutdown()
	s.server.Shutdown()
	if s.inspector != nil {
		_ = s.inspector.Close()
	}
	return nil
}

func wrapAsynqHandler(taskType string, handler func(context.Context, *asynq.Task) error) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, task *asynq.Task) error {
		start := time.Now()
		err := handler(ctx, task)
		metrics.ObserveAsynqTaskDuration(taskType, time.Since(start))
		if err == nil {
			metrics.IncAsynqTask(taskType, "success")
		}
		return err
	}
}

func (s *AsynqServer) startQueueMetricsCollector(ctx context.Context) {
	go func() {
		s.collectQueueMetrics()

		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				s.collectQueueMetrics()
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (s *AsynqServer) collectQueueMetrics() {
	if s.inspector == nil {
		return
	}

	queueSet := make(map[string]struct{}, len(s.queues))
	for _, queue := range s.queues {
		queueSet[queue] = struct{}{}
	}

	queues, err := s.inspector.Queues()
	if err == nil {
		for _, queue := range queues {
			queueSet[queue] = struct{}{}
		}
	}

	for queue := range queueSet {
		info, err := s.inspector.GetQueueInfo(queue)
		if err != nil {
			if errors.Is(err, asynq.ErrQueueNotFound) {
				metrics.SetAsynqQueueStats(queue, 0, 0, 0)
			}
			continue
		}
		metrics.SetAsynqQueueStats(queue, info.Size, info.Retry, info.Scheduled)
	}
}

func asynqTaskResult(err error) string {
	if errors.Is(err, asynq.SkipRetry) {
		return "skip_retry"
	}
	return "error"
}
