package device

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"

	"github.com/labnote/labnote-device-connector/internal/lads"
	"github.com/labnote/labnote-device-connector/internal/model"
)

// This file handles instruments that are plain OPC UA servers: they publish
// their readings as ordinary variables and have no LADS result set. A balance
// such as the Sartorius Cubis exposes CurrentWeight and RegisteredWeight below
// an object called "Simple Scale" — every new RegisteredWeight is one finished
// measurement.

// valueSession watches one trigger variable and turns every new value into a
// measurement. It returns when ctx is cancelled or the session breaks.
func (s *Supervisor) valueSession(ctx context.Context, client *opcua.Client, b *lads.Browser, deviceNode string) error {
	base, err := ua.ParseNodeID(deviceNode)
	if err != nil {
		return fmt.Errorf("device node %q: %w", deviceNode, err)
	}

	trigger, err := s.triggerPath(ctx, b, base)
	if err != nil {
		return err
	}
	extras := s.valuePaths(trigger)
	triggerNode, err := b.NodeForPath(ctx, base, trigger)
	if err != nil {
		return fmt.Errorf("value %q not readable on this instrument: %w", trigger, err)
	}
	s.log.Info("reading plain OPC UA values", "device_node", deviceNode, "trigger", trigger, "extra_values", len(extras))

	// Send the value that is already on the instrument, so a measurement taken
	// while the connector was away is not lost. Duplicates are rejected by
	// LabNote on the stable result id, so this is safe to repeat.
	s.forwardValue(ctx, b, base, trigger, extras)

	notifications := make(chan *opcua.PublishNotificationData, 16)
	sub, err := client.Subscribe(ctx, &opcua.SubscriptionParameters{
		Interval:                   500 * time.Millisecond,
		MaxNotificationsPerPublish: 100,
		LifetimeCount:              2400,
		MaxKeepAliveCount:          20,
	}, notifications)
	if err != nil {
		return fmt.Errorf("create subscription: %w", err)
	}
	defer func() { _ = sub.Cancel(context.WithoutCancel(ctx)) }()

	req := opcua.NewMonitoredItemCreateRequestWithDefaults(triggerNode, ua.AttributeIDValue, 1)
	if _, err := sub.Monitor(ctx, ua.TimestampsToReturnSource, req); err != nil {
		// Not fatal: the poll below still picks up every new value.
		s.log.Warn("live watch on the value failed, polling instead", "error", err)
	}

	keepAlive := time.NewTicker(30 * time.Second)
	defer keepAlive.Stop()
	poll := time.NewTicker(2 * time.Second)
	defer poll.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-poll.C:
			s.forwardValue(ctx, b, base, trigger, extras)
		case <-keepAlive.C:
			if _, err := client.Node(ua.NewNumericNodeID(0, 2258)).Value(ctx); err != nil {
				return fmt.Errorf("keep-alive failed: %w", err)
			}
		case n, ok := <-notifications:
			if !ok {
				return errors.New("notification channel closed")
			}
			if n.Error != nil {
				return fmt.Errorf("subscription error: %w", n.Error)
			}
			switch n.Value.(type) {
			case *ua.DataChangeNotification, *ua.EventNotificationList:
				s.forwardValue(ctx, b, base, trigger, extras)
			}
		}
	}
}

// triggerPath decides which variable means "a new measurement was taken":
// the configured one, else the first one the mapping profile suggests, else the
// first enabled single value, else the first readable numeric variable.
func (s *Supervisor) triggerPath(ctx context.Context, b *lads.Browser, base *ua.NodeID) (string, error) {
	if p := strings.TrimSpace(s.ins.TriggerPath); p != "" {
		return p, nil
	}
	for _, p := range s.profile.ValueTriggerPaths {
		if _, err := b.NodeForPath(ctx, base, p); err == nil {
			return p, nil
		}
	}
	_, values := s.ins.EnabledParameters()
	if len(values) > 0 {
		return values[0], nil
	}
	for _, v := range b.Variables(ctx, base.String()) {
		if v.Kind == "value" {
			return v.Path, nil
		}
	}
	return "", errors.New("this instrument publishes no LADS results and no readable value; choose the value to read in the setup screen")
}

// valuePaths are the additional variables reported alongside the trigger.
func (s *Supervisor) valuePaths(trigger string) []string {
	series, values := s.ins.EnabledParameters()
	out := append([]string{}, values...)
	out = append(out, series...)
	if len(out) == 0 {
		out = append(out, s.profile.ValuePaths...)
	}
	keep := out[:0]
	for _, p := range out {
		if p != "" && p != trigger {
			keep = append(keep, p)
		}
	}
	return keep
}

// forwardValue reads the trigger (plus the extra values) and enqueues one
// measurement, unless that exact reading was already sent.
func (s *Supervisor) forwardValue(ctx context.Context, b *lads.Browser, base *ua.NodeID, trigger string, extras []string) {
	read, err := b.ReadValue(ctx, base, trigger)
	if err != nil {
		s.log.Debug("read value failed", "path", trigger, "error", err)
		return
	}
	if !s.valueGate.accept(read.Values, read.Timestamp) {
		return
	}
	measuredAt := read.Timestamp
	if measuredAt.IsZero() {
		measuredAt = time.Now().UTC()
	}
	// The instrument's own timestamp makes the id stable across reconnects and
	// restarts, so a repeated read is recognised as the same measurement.
	id := fmt.Sprintf("%s:%s", s.ins.ExternalDeviceID, measuredAt.UTC().Format(time.RFC3339Nano))

	s.mu.Lock()
	already := s.seen[id]
	s.seen[id] = true
	s.mu.Unlock()
	if already {
		return
	}

	summary := map[string]any{leafName(trigger): read.Values[0]}
	if len(read.Values) > 1 {
		summary[leafName(trigger)] = read.Values
	}
	unit := read.Unit
	if unit == "" {
		unit = s.ins.DefaultUnitY
	}
	if unit == "" {
		unit = s.profile.DefaultUnitY
	}
	if unit != "" {
		summary["unit"] = unit
	}
	for _, p := range extras {
		extra, err := b.ReadValue(ctx, base, p)
		if err != nil || len(extra.Values) == 0 {
			continue
		}
		if len(extra.Values) == 1 {
			summary[leafName(p)] = extra.Values[0]
		} else {
			summary[leafName(p)] = extra.Values
		}
	}

	rec := model.Result{
		ExternalDeviceID: s.ins.ExternalDeviceID,
		ExternalResultID: id,
		MeasuredAt:       measuredAt.UTC(),
		UnitY:            unit,
		Summary:          summary,
		LADS: map[string]any{
			"source":      "opcua-values",
			"device_node": base.String(),
			"value_path":  trigger,
		},
	}
	if err := s.sink.Enqueue(ctx, rec); err != nil {
		s.log.Error("enqueue result failed", "error", err)
		return
	}
	s.st.RecordResult(s.ins)
	s.log.Info("value queued", "external_result_id", rec.ExternalResultID, "path", trigger)
}

// leafName is the last browse name of a path, used as the summary key.
func leafName(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}
