package queue

import (
	"encoding/json"

	"github.com/hibiken/asynq"
)

const (
	TaskServiceProvision = "service.provision"
	TaskProviderSync     = "provider.sync"
	TaskMailSend         = "mail.send"
	TaskWebhookDeliver   = "webhook.deliver"
	TaskServiceSuspend   = "service.suspend"
	TaskServiceUnsuspend = "service.unsuspend"
	TaskServiceTerminate = "service.terminate"
	TaskServiceRenew     = "service.renew"
	TaskServiceUpgrade   = "service.upgrade"
)

type Client struct{ q *asynq.Client }

func New(addr, password string, db int) *Client {
	return &Client{q: asynq.NewClient(asynq.RedisClientOpt{Addr: addr, Password: password, DB: db})}
}

func (c *Client) Close() error { return c.q.Close() }

func (c *Client) enqueue(task *asynq.Task, queueName string, retries int) error {
	_, err := c.q.Enqueue(task, asynq.MaxRetry(retries), asynq.Queue(queueName))
	return err
}

func payload(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func (c *Client) Provision(serviceID string) error {
	return c.enqueue(asynq.NewTask(TaskServiceProvision, payload(map[string]string{"service_id": serviceID})), "critical", 8)
}

func (c *Client) ProviderSync() error {
	return c.enqueue(asynq.NewTask(TaskProviderSync, nil), "default", 3)
}

// MailPayload 是 mail.send 任务的载荷（Provider 为空 = 跟随当前默认通道）。
type MailPayload struct {
	To       string `json:"to"`
	Subject  string `json:"subject"`
	Body     string `json:"body"`
	Provider string `json:"provider,omitempty"`
}

func (c *Client) MailSend(to, subject, body string) error {
	return c.MailSendVia(to, subject, body, "")
}

// MailSendVia 指定邮件通道发信（provider 为空时使用当前默认通道）。
func (c *Client) MailSendVia(to, subject, body, provider string) error {
	return c.enqueue(asynq.NewTask(TaskMailSend, payload(MailPayload{To: to, Subject: subject, Body: body, Provider: provider})), "default", 5)
}

func (c *Client) WebhookDeliver(deliveryID int64) error {
	return c.enqueue(asynq.NewTask(TaskWebhookDeliver, payload(map[string]int64{"delivery_id": deliveryID})), "default", 6)
}

func (c *Client) ServiceSuspend(serviceID string) error {
	return c.enqueue(asynq.NewTask(TaskServiceSuspend, payload(map[string]string{"service_id": serviceID})), "default", 8)
}

func (c *Client) ServiceUnsuspend(serviceID string) error {
	return c.enqueue(asynq.NewTask(TaskServiceUnsuspend, payload(map[string]string{"service_id": serviceID})), "default", 8)
}

func (c *Client) ServiceTerminate(serviceID string) error {
	return c.enqueue(asynq.NewTask(TaskServiceTerminate, payload(map[string]string{"service_id": serviceID})), "default", 8)
}

func (c *Client) ServiceRenew(serviceID string) error {
	return c.enqueue(asynq.NewTask(TaskServiceRenew, payload(map[string]string{"service_id": serviceID})), "default", 8)
}

// ServiceUpgrade notifies the upstream provider that the service moved to a new
// plan (magic cube server modules call this _ChangePackage).
func (c *Client) ServiceUpgrade(serviceID string) error {
	return c.enqueue(asynq.NewTask(TaskServiceUpgrade, payload(map[string]string{"service_id": serviceID})), "default", 8)
}
