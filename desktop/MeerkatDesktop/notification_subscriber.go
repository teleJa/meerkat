//go:build !android

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log"
	"meerkat/desktop/hub"
	"net/http"
	"strings"
	"time"
)

type desktopNotificationCursor struct {
	lastID string
	recent []recentNotification
}
type recentNotification struct {
	id     int64
	key    string
	expiry time.Time
}

func (c *desktopNotificationCursor) accept(n hub.TurnNotification, now time.Time) bool {
	for len(c.recent) > 0 && !c.recent[0].expiry.After(now) {
		c.recent = c.recent[1:]
	}
	for _, r := range c.recent {
		if r.id == n.ID || r.key == n.DedupKey {
			return false
		}
	}
	if len(c.recent) == 256 {
		c.recent = c.recent[1:]
	}
	c.recent = append(c.recent, recentNotification{n.ID, n.DedupKey, now.Add(10 * time.Minute)})
	return true
}
func runNotificationSubscriber() {
	var cursor desktopNotificationCursor
	delay := time.Second
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 15 * time.Second
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for {
		started := time.Now()
		err := cursor.subscribe(client)
		if time.Since(started) > 25*time.Second {
			delay = time.Second
		}
		if err != nil {
			log.Printf("通知订阅中断：%v", err)
		}
		time.Sleep(delay)
		delay = min(delay*2, 15*time.Second)
	}
}
func (c *desktopNotificationCursor) subscribe(client *http.Client) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := centerURL()
	if appCfg.Mode == "node" {
		var err error
		base, err = waitEdgeLocalAPIURL(ctx)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(base, "/")+"/api/notifications/stream", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+bundledAuthToken())
	req.Header.Set("Last-Event-ID", c.lastID)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New(resp.Status)
	}
	timer := time.AfterFunc(time.Minute, cancel)
	defer timer.Stop()
	scan := bufio.NewScanner(resp.Body)
	var settings hub.NotificationSettings
	var kind, data, id string
	for scan.Scan() {
		timer.Reset(time.Minute)
		line := scan.Text()
		if line != "" {
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				kind = value
			case "data":
				data = value
			case "id":
				id = value
			}
			continue
		}
		switch kind {
		case "settings":
			c.lastID = id
			if err := json.Unmarshal([]byte(data), &settings); err != nil {
				return err
			}
		case "notification":
			var n hub.TurnNotification
			if err := json.Unmarshal([]byte(data), &n); err != nil {
				return err
			}
			c.lastID = id
			now := time.Now()
			eventTime, err := time.Parse(time.RFC3339Nano, n.At)
			quiet := hub.InQuietHours(now, settings) || (err == nil && hub.InQuietHours(eventTime.In(now.Location()), settings))
			bypass := settings.ApprovalBypass && (n.Kind == "bot.approval" || n.Kind == "turn.input_required")
			if c.accept(n, now) && settings.Allows(n.Kind) && (!quiet || bypass) {
				quietForeground := n.Kind == "turn.idle" || n.Kind == "bot.group_decision"
				if !quietForeground || mainWindow == nil || !mainWindow.IsFocused() || mainWindow.IsMinimised() {
					if err := sendTurnNotification(n); err != nil {
						log.Printf("通知投递失败：%v", err)
					}
				}
			}
		}
		kind, data, id = "", "", ""
	}
	return scan.Err()
}
