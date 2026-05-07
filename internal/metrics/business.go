package metrics

import (
	"strconv"
	"time"
)

func IncActivityQuota(activityID int64, result string) {
	ActivityQuotaTotal.WithLabelValues(int64Label(activityID), normalizeLabel(result)).Inc()
}

func IncRaffle(activityID, strategyID int64, result string) {
	RaffleTotal.WithLabelValues(int64Label(activityID), int64Label(strategyID), normalizeLabel(result)).Inc()
}

func ObserveRaffleDuration(activityID, strategyID int64, duration time.Duration) {
	RaffleDuration.WithLabelValues(int64Label(activityID), int64Label(strategyID)).Observe(duration.Seconds())
}

func IncStockConsume(activityID, skuID int64, result string) {
	StockConsumeTotal.WithLabelValues(int64Label(activityID), int64Label(skuID), normalizeLabel(result)).Inc()
}

func IncAward(awardID int, result string) {
	AwardTotal.WithLabelValues(intLabel(awardID), normalizeLabel(result)).Inc()
}

func IncRabbitMQPublish(topic, result string) {
	RabbitMQPublishTotal.WithLabelValues(normalizeLabel(topic), normalizeLabel(result)).Inc()
}

func IncAsynqTask(taskType, result string) {
	AsynqTaskTotal.WithLabelValues(normalizeLabel(taskType), normalizeLabel(result)).Inc()
}

func int64Label(v int64) string {
	if v <= 0 {
		return "unknown"
	}
	return strconv.FormatInt(v, 10)
}

func intLabel(v int) string {
	if v <= 0 {
		return "unknown"
	}
	return strconv.Itoa(v)
}

func normalizeLabel(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}
