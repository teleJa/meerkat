package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

var errPoolBotUnavailable = errors.New("DS Bot is unavailable")

type poolBotDirectory struct {
	MainBotID string `json:"mainBotId"`
	Bots      []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"bots"`
	Rooms []struct {
		ID      string   `json:"id"`
		Name    string   `json:"name"`
		Mode    string   `json:"mode"`
		Admin   string   `json:"admin"`
		Members []string `json:"members"`
	} `json:"rooms"`
}

func (d poolBotDirectory) target(kind, id string) (poolTarget, bool) {
	if id == "" {
		return poolTarget{}, false
	}
	switch kind {
	case "bot":
		for _, bot := range d.Bots {
			if bot.ID == id {
				return poolTarget{Kind: kind, ID: id, ActorID: id}, true
			}
		}
	case "group":
		for _, group := range d.Rooms {
			if group.ID == id && group.Mode == "admin" && slices.Contains(group.Members, group.Admin) {
				if _, found := d.target("bot", group.Admin); found {
					return poolTarget{Kind: kind, ID: id, ActorID: group.Admin}, true
				}
			}
		}
	}
	return poolTarget{}, false
}

type poolBotClient struct {
	endpoint string
	http     *http.Client
}

func (h *Hub) taskPoolBotClient(deviceID string) (*poolBotClient, error) {
	if deviceID == "" {
		return nil, errPoolBotUnavailable
	}
	launch, entry, err := h.ensureHarnessProxyEntry(deviceID)
	if err != nil {
		return nil, errPoolBotUnavailable
	}
	jar, err := entry.jar(launch, nil)
	if err != nil {
		return nil, errPoolBotUnavailable
	}
	endpoint, err := url.Parse(launch)
	if err != nil {
		return nil, errPoolBotUnavailable
	}
	endpoint.Path, endpoint.RawPath, endpoint.RawQuery, endpoint.Fragment = "/api/bot", "", "", ""
	return &poolBotClient{endpoint: endpoint.String(), http: &http.Client{
		Transport:     entry,
		Jar:           jar,
		Timeout:       35 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

type poolBotResponse struct {
	OK    *bool           `json:"ok"`
	Value json.RawMessage `json:"value"`
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

func (c *poolBotClient) call(ctx context.Context, operation string, payload any) (poolBotResponse, error) {
	var result poolBotResponse
	body, err := json.Marshal(map[string]any{"endpoint": operation, "payload": payload})
	if err != nil {
		return result, errPoolBotUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return result, errPoolBotUnavailable
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return result, errPoolBotUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		no := false
		result.OK, result.Error.Code = &no, "dsh_auth_rejected"
		return result, nil
	}
	if response.StatusCode != http.StatusOK {
		return result, errPoolBotUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(data) > 4<<20 || json.Unmarshal(data, &result) != nil || result.OK == nil {
		return poolBotResponse{}, errPoolBotUnavailable
	}
	return result, nil
}

func (c *poolBotClient) directory(ctx context.Context) (poolBotDirectory, error) {
	var directory poolBotDirectory
	result, err := c.call(ctx, "state", struct{}{})
	if err != nil || !*result.OK || json.Unmarshal(result.Value, &directory) != nil {
		return directory, errPoolBotUnavailable
	}
	return directory, nil
}

func (c *poolBotClient) send(ctx context.Context, target, purpose, cardURL string) (status, code string) {
	notice := "Meerkat 有一项任务已分派，请在卡片查看并回报进展。"
	switch purpose {
	case "assign_requested":
		notice = "Meerkat 有一项任务待分派，请打开任务卡。"
	case "returned":
		notice = "Meerkat 有一项任务已打回，请打开任务卡查看原因。"
	}
	result, err := c.call(ctx, "send", map[string]string{
		"sessionId": target,
		"text":      notice + "\n" + strings.ReplaceAll(cardURL, "@", "%40"),
	})
	if err != nil {
		return "uncertain", "delivery_unconfirmed"
	}
	if *result.OK {
		return "queued", ""
	}
	switch result.Error.Code {
	case "bot/invalid", "bot/refused", "bot/stale", "dsh_auth_rejected":
		return "failed", result.Error.Code
	default:
		return "uncertain", "delivery_unconfirmed"
	}
}
