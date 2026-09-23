package printer

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/voska/bambu/internal/errfmt"
)

// Conn is a live MQTT session with one printer. The concrete type is swappable for tests.
type Conn interface {
	// Pushall requests a full status report and returns the merged print object.
	Pushall(ctx context.Context) (map[string]any, error)
	// State returns a copy of the merged print object received so far.
	State() map[string]any
	// Updates is signalled (non-blocking) whenever a push_status report arrives.
	Updates() <-chan struct{}
	// Command publishes {section: body} at QoS 1 and waits (until ctx is done) for the printer's reply
	// to that command. It returns nil, nil if no reply arrived.
	Command(ctx context.Context, section string, body map[string]any) (map[string]any, error)
	Close()
}

// Dial connects to the printer's MQTT broker (mqtts://host:8883, user bblp, password = access code).
func Dial(ctx context.Context, host, serial, code string) (Conn, error) {
	c := &mqttConn{serial: serial, state: map[string]any{}, updates: make(chan struct{}, 1), full: make(chan struct{}, 1)}
	opts := mqtt.NewClientOptions().
		AddBroker("ssl://" + host + ":8883").
		SetClientID(fmt.Sprintf("bambu-%d", time.Now().UnixNano()%1e9)).
		SetUsername("bblp").SetPassword(code).
		SetTLSConfig(&tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}). //nolint:gosec // printers use self-signed certs
		SetConnectTimeout(15 * time.Second).
		SetAutoReconnect(false).
		SetOrderMatters(false).
		SetDefaultPublishHandler(c.onMessage)
	c.client = mqtt.NewClient(opts)
	tok := c.client.Connect()
	if !waitToken(ctx, tok, 20*time.Second) {
		return nil, errfmt.New(errfmt.ExitRetryable, "MQTT connect to %s:8883 timed out", host).
			WithHint("check the printer is on, reachable from this machine, and in LAN mode")
	}
	if err := tok.Error(); err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "not authorized") || strings.Contains(msg, "bad user name or password") {
			return nil, errfmt.Wrap(errfmt.ExitAuth, err, "printer refused the access code").
				WithHint("the code changes when LAN Only / Developer Mode is toggled; read it on the printer and run: bambu auth set <printer>")
		}
		return nil, errfmt.Wrap(errfmt.ExitRetryable, err, "MQTT connect to %s:8883 failed", host).
			WithHint("check the host in config and that the printer is on the network")
	}
	sub := c.client.Subscribe("device/"+serial+"/report", 0, nil)
	if !waitToken(ctx, sub, 10*time.Second) || sub.Error() != nil {
		c.Close()
		return nil, errfmt.New(errfmt.ExitRetryable, "MQTT subscribe failed").WithHint("retry; if it persists, check the serial in config")
	}
	return c, nil
}

type mqttConn struct {
	client  mqtt.Client
	serial  string
	mu      sync.Mutex
	state   map[string]any
	replies []map[string]any
	updates chan struct{}
	full    chan struct{}
	seq     atomic.Int64
}

func (c *mqttConn) onMessage(_ mqtt.Client, m mqtt.Message) {
	var d map[string]any
	if json.Unmarshal(m.Payload(), &d) != nil {
		return
	}
	c.mu.Lock()
	p, isPrint := d["print"].(map[string]any)
	switch {
	case isPrint && p["command"] == "push_status":
		DeepMerge(c.state, p)
		c.mu.Unlock()
		if _, ok := p["gcode_state"]; ok {
			signal(c.full)
		}
		signal(c.updates)
		return
	default:
		c.replies = append(c.replies, d)
		if len(c.replies) > 256 {
			c.replies = c.replies[len(c.replies)-256:]
		}
	}
	c.mu.Unlock()
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (c *mqttConn) publish(ctx context.Context, payload map[string]any, qos byte) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return errfmt.Wrap(errfmt.ExitError, err, "encode MQTT payload")
	}
	tok := c.client.Publish("device/"+c.serial+"/request", qos, false, b)
	if !waitToken(ctx, tok, 10*time.Second) {
		return errfmt.New(errfmt.ExitRetryable, "MQTT publish timed out")
	}
	if err := tok.Error(); err != nil {
		return errfmt.Wrap(errfmt.ExitRetryable, err, "MQTT publish failed")
	}
	return nil
}

func (c *mqttConn) Pushall(ctx context.Context) (map[string]any, error) {
	select { // drain a stale signal
	case <-c.full:
	default:
	}
	if err := c.publish(ctx, map[string]any{"pushing": map[string]any{"sequence_id": c.nextSeq(), "command": "pushall"}}, 0); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	select {
	case <-c.full:
		return c.State(), nil
	case <-ctx.Done():
		return nil, errfmt.New(errfmt.ExitRetryable, "no status report from printer").WithHint("retry; if it persists, power-cycle the printer")
	}
}

func (c *mqttConn) State() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return deepCopy(c.state)
}

func (c *mqttConn) Updates() <-chan struct{} { return c.updates }

func (c *mqttConn) nextSeq() string {
	return strconv.FormatInt(time.Now().Unix()%1e6*1000+c.seq.Add(1)%1000, 10)
}

func (c *mqttConn) Command(ctx context.Context, section string, body map[string]any) (map[string]any, error) {
	seq := c.nextSeq()
	payload := map[string]any{}
	inner := map[string]any{"sequence_id": seq}
	for k, v := range body {
		if k != "sequence_id" {
			inner[k] = v
		}
	}
	payload[section] = inner
	c.mu.Lock()
	c.replies = nil
	c.mu.Unlock()
	if err := c.publish(ctx, payload, 1); err != nil {
		return nil, err
	}
	cmd := body["command"]
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		c.mu.Lock()
		for _, r := range c.replies {
			if s, ok := r[section].(map[string]any); ok && s["command"] == cmd {
				if rs := str(s["sequence_id"]); rs == "" || rs == seq {
					c.mu.Unlock()
					return s, nil
				}
			}
		}
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, nil //nolint:nilnil // "no reply" is a normal outcome
		case <-tick.C:
		}
	}
}

func (c *mqttConn) Close() {
	if c.client != nil && c.client.IsConnected() {
		c.client.Disconnect(250)
	}
}

func waitToken(ctx context.Context, t mqtt.Token, d time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	select {
	case <-t.Done():
		return true
	case <-ctx.Done():
		return false
	}
}

// CheckAck interprets a command reply. A missing reply is not an error (the caller verifies state instead).
func CheckAck(ack map[string]any, what string) error {
	if ack == nil {
		return nil
	}
	result := strings.ToLower(str(ack["result"]))
	if result == "" || result == "success" || result == "ok" {
		return nil
	}
	reason := str(ack["reason"])
	if reason == "" {
		reason = str(ack["err_code"])
	}
	lr := strings.ToLower(reason)
	if strings.Contains(lr, "verif") || strings.Contains(lr, "sign") || strings.Contains(lr, "auth") {
		return errfmt.New(errfmt.ExitForbidden, "%s rejected by printer: %s", what, reason).
			WithHint("enable Developer Mode on the printer (Settings > LAN Only > Developer Mode)").WithData("ack", ack)
	}
	return errfmt.New(errfmt.ExitError, "%s rejected by printer: result=%s reason=%s", what, result, reason).
		WithHint("check the printer screen and `bambu status`").WithData("ack", ack)
}

// DeepMerge merges src into dst recursively (push_status reports can be partial).
func DeepMerge(dst, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := dst[k].(map[string]any); ok {
				DeepMerge(dm, sm)
				continue
			}
		}
		dst[k] = v
	}
}

func deepCopy(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	if out == nil {
		out = map[string]any{}
	}
	return out
}
