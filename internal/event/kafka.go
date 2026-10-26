package event

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/segmentio/kafka-go"
)

type kafkaWriter interface {
	WriteMessages(ctx context.Context, msgs ...kafka.Message) error
	Close() error
}

// KafkaPublisher 用 segmentio/kafka-go 写事件。
type KafkaPublisher struct {
	writer kafkaWriter
}

// NewKafkaPublisher 创建 writer；topic 自动创建依赖 broker 配置。
func NewKafkaPublisher(brokers []string, topic string, timeout time.Duration) (*KafkaPublisher, error) {
	if len(brokers) == 0 || topic == "" {
		return nil, fmt.Errorf("event: kafka brokers/topic 不能为空")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("event: kafka timeout 必须为正数")
	}
	return &KafkaPublisher{writer: &kafka.Writer{
		Addr:         kafka.TCP(brokers...),
		Topic:        topic,
		Balancer:     &kafka.Hash{},
		RequiredAcks: kafka.RequireOne,
		Async:        false,
		BatchTimeout: 10 * time.Millisecond,
		WriteTimeout: timeout,
		ReadTimeout:  timeout,
	}}, nil
}

func (p *KafkaPublisher) Publish(ctx context.Context, ev Event) error {
	raw, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("event: marshal: %w", err)
	}
	return p.writer.WriteMessages(ctx, kafka.Message{
		Key:   ev.DocumentID[:],
		Value: raw,
		Time:  time.Now().UTC(),
	})
}

func (p *KafkaPublisher) Close() error { return p.writer.Close() }

type messageReader interface {
	FetchMessage(ctx context.Context) (kafka.Message, error)
	CommitMessages(ctx context.Context, msgs ...kafka.Message) error
	Close() error
}

// Consumer 从 Kafka 读事件并交给 Handler；只有 Handler 成功才提交 offset。
type Consumer struct {
	reader  messageReader
	handler func(context.Context, Event) error
}

// NewConsumer 创建 consumer。
func NewConsumer(brokers []string, topic, groupID string, handler func(context.Context, Event) error) (*Consumer, error) {
	if len(brokers) == 0 || topic == "" || groupID == "" {
		return nil, fmt.Errorf("event: kafka brokers/topic/group_id 不能为空")
	}
	if handler == nil {
		return nil, fmt.Errorf("event: handler 不能为空")
	}
	return &Consumer{
		reader: kafka.NewReader(kafka.ReaderConfig{
			Brokers:        brokers,
			Topic:          topic,
			GroupID:        groupID,
			MinBytes:       1,
			MaxBytes:       10 << 20,
			MaxWait:        500 * time.Millisecond,
			CommitInterval: 0, // 手动提交：Handler 成功后才提交
		}),
		handler: handler,
	}, nil
}

// Run 阻塞消费；ctx 取消时返回 nil。
func (c *Consumer) Run(ctx context.Context) error {
	defer func() { _ = c.reader.Close() }()
	for {
		msg, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("event: fetch: %w", err)
		}
		var ev Event
		if err := json.Unmarshal(msg.Value, &ev); err != nil {
			// 毒消息不阻塞 partition；记录后提交，避免无限重试。
			_ = c.reader.CommitMessages(ctx, msg)
			continue
		}
		if err := c.handler(ctx, ev); err != nil {
			// 不提交，交给下一次 poll 重试；真实系统应加退避/死信。
			continue
		}
		if err := c.reader.CommitMessages(ctx, msg); err != nil {
			return fmt.Errorf("event: commit: %w", err)
		}
	}
}

// Close 关闭 reader（测试/主动退出用）。
func (c *Consumer) Close() error { return c.reader.Close() }
