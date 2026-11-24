package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strings"
	"time"
)

type teamRoom struct {
	ID, Name, Admin, SessionID string
	Members, Parts             []string
}
type teamBot struct {
	ID, Name, SessionID string
	Parts               []string
}
type noticeCard struct{ ID, Question, Title string }
type noticeState struct {
	MainBotID        string
	MainBotIDs       []string
	Bots             []teamBot
	Rooms            []teamRoom
	Questions, Views map[string]noticeCard
	Work             struct{ Rooms map[string]json.RawMessage }
}
type botNotice struct {
	Seq                                               int
	Kind, Name, BotID, RoomID, SessionID, Title, Text string
}
type noticeBatch struct {
	Boot    string
	Seq     int
	Reset   bool
	Notices []botNotice
}
type noticeTracker struct {
	boot    string
	seq     int
	hidden  map[string]bool
	cards   map[string]bool
	retry   time.Time
	pending map[string]pendingGroup
}
type pendingGroup struct {
	notice botNotice
	due    time.Time
}

func (c *poolBotClient) readNotice(ctx context.Context, operation string, payload, out any) bool {
	res, err := c.call(ctx, operation, payload)
	return err == nil && res.OK != nil && *res.OK && json.Unmarshal(res.Value, out) == nil
}
func (d *deviceWatch) pollNotices(ctx context.Context) {
	t := &d.tracker
	if time.Now().Before(t.retry) {
		return
	}
	c, err := d.hub.taskPoolBotClient(d.deviceID)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	var batch noticeBatch
	if !c.readNotice(ctx, "notices", map[string]any{"boot": t.boot, "since": t.seq}, &batch) {
		t.retry = time.Now().Add(30 * time.Second)
		return
	}
	var state noticeState
	if !c.readNotice(ctx, "state", struct{}{}, &state) {
		return
	}
	baseline := t.boot != batch.Boot || batch.Reset
	hidden := map[string]bool{}
	roomBySession := map[string]teamRoom{}
	for _, b := range state.Bots {
		hidden[b.SessionID] = true
		for _, id := range b.Parts {
			hidden[id] = true
		}
	}
	for _, r := range state.Rooms {
		for _, id := range append(slices.Clone(r.Parts), r.SessionID) {
			hidden[id] = true
			roomBySession[id] = r
		}
	}
	t.hidden = hidden
	cards := map[string]bool{}
	for _, set := range []map[string]noticeCard{state.Questions, state.Views} {
		for session, card := range set {
			room, ok := roomBySession[session]
			if !ok || card.ID == "" {
				continue
			}
			key := "dsbot:card:" + d.deviceID + ":" + session + ":" + card.ID
			cards[key] = true
			if !baseline && !t.cards[key] {
				d.publishBot(botNotice{SessionID: session, Title: room.Name, Text: card.Question}, "action_required", key, 10*time.Minute)
			}
		}
	}
	t.cards = cards
	if baseline {
		t.pending = map[string]pendingGroup{}
	} else {
		for _, n := range batch.Notices {
			if n.Seq <= t.seq {
				continue
			}
			key := fmt.Sprintf("dsbot:%s:%s:%s:%d", n.Kind, d.deviceID, batch.Boot, n.Seq)
			switch n.Kind {
			case "approval":
				d.publishBot(n, "approval", key, 5*time.Minute)
			case "question", "view":
				d.publishBot(n, "action_required", key, 5*time.Minute)
			case "message":
				if state.isChief(n) {
					d.publishBot(n, "chief_dm", "dsbot:dm:"+d.deviceID+":"+n.BotID, 15*time.Second)
				}
			case "group":
				if explicitUser(n.Text) {
					d.publishGroup(n, "chief_mention")
				} else if state.isChief(n) || slices.ContainsFunc(state.Rooms, func(room teamRoom) bool { return room.ID == n.RoomID && room.Admin == n.BotID }) {
					t.pending[n.RoomID] = pendingGroup{n, time.Now().Add(3 * time.Second)}
				} else if strings.HasSuffix(n.Text, "…") {
					d.classifyGroup(ctx, c, state, n)
				}
			}
		}
	}
	t.boot, t.seq = batch.Boot, batch.Seq
	for id, p := range t.pending {
		if time.Now().Before(p.due) {
			continue
		}
		delete(t.pending, id)
		d.classifyGroup(ctx, c, state, p.notice)
	}
}
func (s noticeState) isChief(n botNotice) bool {
	return n.BotID == s.MainBotID || slices.Contains(s.MainBotIDs, n.BotID) || strings.EqualFold(n.Name, "Chief")
}
func (d *deviceWatch) publishBot(n botNotice, kind, key string, ttl time.Duration) {
	title := n.Title
	if title == "" {
		title = n.Name
	}
	d.hub.publishNotification(TurnNotification{Kind: "bot." + kind, DeviceID: d.deviceID, SessionID: n.SessionID, SessionTitle: title, Excerpt: truncateRunes(n.Text, 120), DedupKey: key}, ttl)
}
func (d *deviceWatch) publishGroup(n botNotice, kind string) {
	d.publishBot(n, kind, "dsbot:group_need_user:"+d.deviceID+":"+n.RoomID, 30*time.Second)
}

var codeSpan = regexp.MustCompile("(?s)```.*?(?:```|$)|`[^`]*`")
var humanMention = regexp.MustCompile(`(?i)@(老板|你|用户|所有人|tele\b|all\b|everyone\b)|\[(待你决策|待验收|需确认)\]`)

func explicitUser(text string) bool {
	clean := codeSpan.ReplaceAllString(text, "")
	return humanMention.MatchString(clean) || strings.HasPrefix(clean, "Asked: ")
}

var decisionWords = regexp.MustCompile(`请.{0,8}(确认|选择|验收|插|解锁|扫码|提供|授权)|等你|需要你|你.{0,8}(拍板|决定|确认|验收)|[？?]$|阻塞|无法继续`)

func (d *deviceWatch) classifyGroup(ctx context.Context, c *poolBotClient, state noticeState, n botNotice) {
	var room teamRoom
	for _, r := range state.Rooms {
		if r.ID == n.RoomID {
			room = r
			break
		}
	}
	admin := state.isChief(n) || room.Admin == n.BotID
	if !admin && !strings.HasSuffix(n.Text, "…") {
		return
	}
	var chat struct {
		Running bool
		Items   []struct{ BotID, Text string }
	}
	if !c.readNotice(ctx, "room-chat", map[string]string{"roomId": n.RoomID}, &chat) {
		return
	}
	prefix := strings.TrimSuffix(n.Text, "…")
	var contextLines []string
	found := false
	for i := len(chat.Items) - 1; i >= 0; i-- {
		line := chat.Items[i]
		if line.BotID == n.BotID && strings.HasPrefix(collapseWhitespace(line.Text), prefix) {
			n.Text = line.Text
			for _, prior := range chat.Items[max(0, i-2):i] {
				contextLines = append(contextLines, truncateRunes(prior.Text, 120))
			}
			found = true
			break
		}
	}
	if !found {
		return
	}
	if explicitUser(n.Text) {
		d.publishGroup(n, "chief_mention")
		return
	}
	if !admin || chat.Running {
		return
	}
	if raw := string(state.Work.Rooms[n.RoomID]); raw != "" && raw != "null" && raw != "[]" && raw != "{}" {
		return
	}
	clean := codeSpan.ReplaceAllString(n.Text, "")
	for _, bot := range state.Bots {
		if slices.Contains(room.Members, bot.ID) && bot.ID != n.BotID && strings.Contains(strings.ToLower(clean), "@"+strings.ToLower(bot.Name)) {
			return
		}
	}
	switch strings.TrimSpace(clean) {
	case "收到", "好的", "我先查一下", "正在看":
		return
	}
	s, err := d.hub.notificationSettings()
	if err != nil || !s.Allows("bot.group_decision") {
		return
	}
	notify, source := classifyDecision(ctx, clean, contextLines)
	log.Printf("通知决策 room=%s seq=%d notify=%t source=%s", room.ID, n.Seq, notify, source)
	if notify {
		d.publishGroup(n, "group_decision")
	}
}
