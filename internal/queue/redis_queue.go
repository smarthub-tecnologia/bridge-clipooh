package queue

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

var ErrDiscardJob = errors.New("discard job without retry")

type Job struct {
	ID          string    `json:"id"`
	PayloadJSON []byte    `json:"payload"`
	RetriesLeft int       `json:"retries_left"`
	NextRetryAt time.Time `json:"next_retry_at"`
}

type RedisQueue struct {
	client    *redis.Client
	streamKey string
	groupName string
}

func NewRedisQueue(client *redis.Client, streamKey string) *RedisQueue {
	ctx := context.Background()
	// Cria consumer group
	if err := client.XGroupCreateMkStream(ctx, streamKey, "wa-bridge-workers", "$").Err(); err != nil {
		if !strings.Contains(err.Error(), "BUSYGROUP") {
			zap.L().Error("failed to create consumer group on init", zap.Error(err))
		}
	}
	return &RedisQueue{client: client, streamKey: streamKey, groupName: "wa-bridge-workers"}
}

func (q *RedisQueue) Enqueue(ctx context.Context, job Job) error {
	data, _ := json.Marshal(job)
	return q.client.XAdd(ctx, &redis.XAddArgs{
		Stream: q.streamKey,
		Values: map[string]interface{}{
			"id":           job.ID,
			"payload":      string(data),
			"retries_left": job.RetriesLeft,
		},
	}).Err()
}

// Consume consome mensagens e chama handler. Em caso de falha, agenda retry.
func (q *RedisQueue) Consume(ctx context.Context, handler func(Job) error) {
	for {
		streams, err := q.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    q.groupName,
			Consumer: "worker-1",
			Streams:  []string{q.streamKey, ">"},
			Count:    1,
			Block:    5 * time.Second,
		}).Result()
		if err != nil {
			if err != redis.Nil {
				zap.L().Error("redis stream read error", zap.Error(err))
				if strings.Contains(err.Error(), "NOGROUP") {
					zap.L().Info("recreating consumer group after NOGROUP error")
					q.client.XGroupCreateMkStream(ctx, q.streamKey, q.groupName, "$")
				}
			}
			time.Sleep(1 * time.Second)
			continue
		}
		for _, msg := range streams[0].Messages {
			var job Job
			payloadStr := msg.Values["payload"].(string)
			if err := json.Unmarshal([]byte(payloadStr), &job); err != nil {
				zap.L().Error("invalid job format", zap.Error(err))
				q.client.XAck(ctx, q.streamKey, q.groupName, msg.ID)
				continue
			}
			if err := handler(job); err != nil {
				if errors.Is(err, ErrDiscardJob) {
					zap.L().Warn("job discarded without retry", zap.String("job_id", job.ID))
					q.client.XAck(ctx, q.streamKey, q.groupName, msg.ID)
					continue
				}

				zap.L().Error("worker process error", zap.Error(err), zap.String("job_id", job.ID))
				zap.L().Warn("job failed, scheduling retry",
					zap.String("job_id", job.ID),
					zap.Int("retries_left", job.RetriesLeft),
				)
				if job.RetriesLeft > 0 {
					job.RetriesLeft--
					// Backoff exponencial: 1s, 2s, 4s, ...
					delay := time.Duration(1<<(3-job.RetriesLeft)) * time.Second
					job.NextRetryAt = time.Now().Add(delay)
					score := float64(job.NextRetryAt.Unix())
					q.client.ZAdd(ctx, "retry:queue", redis.Z{Score: score, Member: job.ID})
					data, _ := json.Marshal(job)
					q.client.HSet(ctx, "job:"+job.ID, "data", data)
				}
			}
			q.client.XAck(ctx, q.streamKey, q.groupName, msg.ID)
		}
	}
}

// ProcessRetries verifica periodicamente e reenvia jobs cujo retry expirou
func (q *RedisQueue) ProcessRetries(ctx context.Context, handler func(Job) error) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			now := time.Now().Unix()
			ids, _ := q.client.ZRangeByScore(ctx, "retry:queue", &redis.ZRangeBy{
				Min: "0", Max: strconv.FormatInt(now, 10),
			}).Result()
			for _, id := range ids {
				data, _ := q.client.HGet(ctx, "job:"+id, "data").Result()
				var job Job
				if err := json.Unmarshal([]byte(data), &job); err != nil {
					continue
				}
				if err := handler(job); err != nil {
					if errors.Is(err, ErrDiscardJob) {
						zap.L().Warn("job discarded without retry (retry queue)", zap.String("job_id", job.ID))
						q.client.ZRem(ctx, "retry:queue", id)
						q.client.HDel(ctx, "job:"+id)
						continue
					}

					zap.L().Error("worker process error (retry)", zap.Error(err), zap.String("job_id", job.ID))
					// Se ainda tem retries, atualiza score para próximo retry
					if job.RetriesLeft > 0 {
						job.RetriesLeft--
						delay := time.Duration(1<<(3-job.RetriesLeft)) * time.Second
						job.NextRetryAt = time.Now().Add(delay)
						score := float64(job.NextRetryAt.Unix())
						q.client.ZAdd(ctx, "retry:queue", redis.Z{Score: score, Member: id})
						data, _ := json.Marshal(job)
						q.client.HSet(ctx, "job:"+id, "data", data)
					}
				} else {
					q.client.ZRem(ctx, "retry:queue", id)
					q.client.HDel(ctx, "job:"+id)
				}
			}
		case <-ctx.Done():
			return
		}
	}
}
