package alerting

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/smtp"
	"strings"
	"time"
)

// Notifier sendet Alert-Benachrichtigungen
type Notifier struct {
	channels map[string]Channel
	client   *http.Client
	logger   *slog.Logger
}

func NewNotifier(channels map[string]Channel) *Notifier {
	return &Notifier{
		channels: channels,
		client:   &http.Client{Timeout: 10 * time.Second},
		logger:   slog.Default(),
	}
}

// Send sendet eine Benachrichtigung an die angegebenen Kanäle
func (n *Notifier) Send(event AlertEvent, channelNames []string) {
	for _, name := range channelNames {
		ch, ok := n.channels[name]
		if !ok {
			n.logger.Warn("unknown notification channel", "name", name)
			continue
		}
		go func(ch Channel, name string) {
			if err := n.sendToChannel(ch, event); err != nil {
				n.logger.Warn("notification failed", "channel", name, "err", err)
			}
		}(ch, name)
	}
}

func (n *Notifier) sendToChannel(ch Channel, e AlertEvent) error {
	switch ch.Type {
	case "slack":
		return n.sendSlack(ch, e)
	case "webhook":
		return n.sendWebhook(ch, e)
	case "email":
		return n.sendEmail(ch, e)
	case "log", "":
		n.logAlert(e)
		return nil
	default:
		return fmt.Errorf("unknown channel type: %s", ch.Type)
	}
}

// ── Slack ─────────────────────────────────────────────────────────────────

func (n *Notifier) sendSlack(ch Channel, e AlertEvent) error {
	if ch.WebhookURL == "" {
		return fmt.Errorf("slack: webhook_url is empty")
	}

	emoji := map[Severity]string{
		SevInfo:     ":information_source:",
		SevWarning:  ":warning:",
		SevCritical: ":fire:",
	}[e.Severity]

	color := map[Severity]string{
		SevInfo:     "#36a64f",
		SevWarning:  "#f0a030",
		SevCritical: "#f04040",
	}[e.Severity]

	stateStr := string(e.State)
	if e.State == StateResolved {
		color = "#36a64f"
		emoji = ":white_check_mark:"
		stateStr = "RESOLVED"
	}

	payload := map[string]any{
		"attachments": []map[string]any{{
			"color":    color,
			"fallback": fmt.Sprintf("[%s] %s on %s", strings.ToUpper(stateStr), e.RuleName, e.Host),
			"title":    fmt.Sprintf("%s %s — %s", emoji, e.RuleName, strings.ToUpper(stateStr)),
			"fields": []map[string]any{
				{"title": "Host",      "value": e.Host,                              "short": true},
				{"title": "Metric",    "value": e.Metric,                            "short": true},
				{"title": "Value",     "value": fmt.Sprintf("%.2f", e.Value),        "short": true},
				{"title": "Threshold", "value": fmt.Sprintf("%.2f", e.Threshold),    "short": true},
				{"title": "Severity",  "value": string(e.Severity),                  "short": true},
				{"title": "Message",   "value": e.Message,                           "short": false},
			},
			"footer": "PulseWatch",
			"ts":     e.FiredAt.Unix(),
		}},
	}

	return n.postJSON(ch.WebhookURL, payload)
}

// ── Generic Webhook ───────────────────────────────────────────────────────

func (n *Notifier) sendWebhook(ch Channel, e AlertEvent) error {
	if ch.WebhookURL == "" {
		return fmt.Errorf("webhook: webhook_url is empty")
	}
	return n.postJSON(ch.WebhookURL, e)
}

// ── Email (SMTP) ──────────────────────────────────────────────────────────

func (n *Notifier) sendEmail(ch Channel, e AlertEvent) error {
	if ch.SMTPHost == "" {
		return fmt.Errorf("email: smtp_host is empty")
	}
	if len(ch.To) == 0 {
		return fmt.Errorf("email: no recipients")
	}

	port := ch.SMTPPort
	if port == 0 {
		port = 587
	}
	addr := fmt.Sprintf("%s:%d", ch.SMTPHost, port)

	subject := fmt.Sprintf("[PulseWatch %s] %s on %s", strings.ToUpper(string(e.Severity)), e.RuleName, e.Host)
	body := fmt.Sprintf(
		"Alert: %s\nHost: %s\nMetric: %s\nValue: %.3f\nThreshold: %.3f\nState: %s\nSeverity: %s\nMessage: %s\nTime: %s\n",
		e.RuleName, e.Host, e.Metric, e.Value, e.Threshold,
		e.State, e.Severity, e.Message, e.FiredAt.Format(time.RFC3339),
	)

	msg := []byte("To: " + strings.Join(ch.To, ", ") + "\r\n" +
		"From: " + ch.From + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
		body)

	var auth smtp.Auth
	if ch.SMTPUser != "" {
		auth = smtp.PlainAuth("", ch.SMTPUser, ch.SMTPPass, ch.SMTPHost)
	}

	// TLS auf Port 465, STARTTLS auf anderen Ports
	if port == 465 {
		tlsConf := &tls.Config{ServerName: ch.SMTPHost}
		conn, err := tls.Dial("tcp", addr, tlsConf)
		if err != nil {
			return fmt.Errorf("smtp tls dial: %w", err)
		}
		defer conn.Close()
		c, err := smtp.NewClient(conn, ch.SMTPHost)
		if err != nil {
			return err
		}
		if auth != nil {
			if err := c.Auth(auth); err != nil {
				return err
			}
		}
		if err := c.Mail(ch.From); err != nil {
			return err
		}
		for _, to := range ch.To {
			c.Rcpt(to)
		}
		w, err := c.Data()
		if err != nil {
			return err
		}
		w.Write(msg)
		w.Close()
		return c.Quit()
	}

	// STARTTLS
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("smtp dial: %w", err)
	}
	c, err := smtp.NewClient(conn, ch.SMTPHost)
	if err != nil {
		return err
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok {
		c.StartTLS(&tls.Config{ServerName: ch.SMTPHost})
	}
	if auth != nil {
		c.Auth(auth)
	}
	if err := smtp.SendMail(addr, auth, ch.From, ch.To, msg); err != nil {
		return fmt.Errorf("smtp send: %w", err)
	}
	return nil
}

// ── Log Fallback ──────────────────────────────────────────────────────────

func (n *Notifier) logAlert(e AlertEvent) {
	n.logger.Warn("🔔 ALERT",
		"rule", e.RuleName,
		"host", e.Host,
		"metric", e.Metric,
		"value", fmt.Sprintf("%.3f", e.Value),
		"threshold", fmt.Sprintf("%.3f", e.Threshold),
		"state", e.State,
		"severity", e.Severity,
	)
}

// ── Helpers ───────────────────────────────────────────────────────────────

func (n *Notifier) postJSON(url string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := n.client.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	return nil
}
