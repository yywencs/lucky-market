package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	HTTPRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "bigmarket",
			Subsystem: "http",
			Name:      "requests_total",
			Help:      "Total number of HTTP requests.",
		},
		[]string{"method", "path", "code"},
	)

	HTTPRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "bigmarket",
			Subsystem: "http",
			Name:      "request_duration_seconds",
			Help:      "HTTP request latency.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)

	ActivityQuotaTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "bigmarket",
			Subsystem: "activity",
			Name:      "quota_total",
			Help:      "Total activity quota check results.",
		},
		[]string{"activity_id", "result"},
	)

	RaffleTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "bigmarket",
			Subsystem: "raffle",
			Name:      "total",
			Help:      "Total raffle attempts.",
		},
		[]string{"activity_id", "strategy_id", "result"},
	)

	RaffleDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "bigmarket",
			Subsystem: "raffle",
			Name:      "duration_seconds",
			Help:      "Raffle execution latency.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"activity_id", "strategy_id"},
	)

	AwardTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "bigmarket",
			Subsystem: "award",
			Name:      "total",
			Help:      "Total award dispatch results.",
		},
		[]string{"award_id", "result"},
	)

	StockConsumeTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "bigmarket",
			Subsystem: "stock",
			Name:      "consume_total",
			Help:      "Stock consume results.",
		},
		[]string{"activity_id", "sku_id", "result"},
	)

	RabbitMQPublishTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "bigmarket",
			Subsystem: "rabbitmq",
			Name:      "publish_total",
			Help:      "Total RabbitMQ publish results.",
		},
		[]string{"topic", "result"},
	)

	AsynqTaskTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "bigmarket",
			Subsystem: "asynq",
			Name:      "task_total",
			Help:      "Total Asynq task execution results.",
		},
		[]string{"task_type", "result"},
	)
)

func init() {
	prometheus.MustRegister(
		HTTPRequestsTotal,
		HTTPRequestDuration,
		ActivityQuotaTotal,
		RaffleTotal,
		RaffleDuration,
		AwardTotal,
		StockConsumeTotal,
		RabbitMQPublishTotal,
		AsynqTaskTotal,
	)
}
