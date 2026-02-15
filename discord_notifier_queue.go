package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

func (n *discordNotifier) noticePrefix() string {
	if n == nil || n.s == nil {
		return "[goPool] "
	}
	cfg := n.s.Config()

	// Prefer the stable coinbase-based tag when available, but fall back to
	// PoolTagPrefix so config reloads (which may omit CoinbaseMsg) still keep a
	// distinct tag in Discord messages.
	tag := displayPoolTagFromCoinbaseMessage(cfg.CoinbaseMsg)
	if tag == "" {
		brand := poolSoftwareName
		if cfg.PoolTagPrefix != "" {
			brand = cfg.PoolTagPrefix + "-" + brand
		}
		tag = "/" + brand + "/"
	}
	// For Discord notices, use a compact bracket tag without slashes.
	tag = strings.Trim(tag, "/")
	if tag == "" {
		tag = poolSoftwareName
	}
	return "[" + tag + "] "
}

func (n *discordNotifier) enqueueNotice(msg string) {
	if n == nil {
		return
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	n.enqueueQueuedLine("", n.noticePrefix()+msg, false)
}

func (n *discordNotifier) enqueuePing(discordUserID, line string) {
	if n == nil {
		return
	}
	discordUserID = strings.TrimSpace(discordUserID)
	line = strings.TrimSpace(line)
	if discordUserID == "" || line == "" {
		return
	}
	n.enqueueQueuedLine(discordUserID, line, false)
}

func (n *discordNotifier) enqueueEveryoneNotice(line string) {
	if n == nil {
		return
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	n.enqueueQueuedLine("", line, true)
}

func (n *discordNotifier) enqueueQueuedLine(mentionUserID, line string, mentionEveryone bool) {
	if n == nil {
		return
	}
	mentionUserID = strings.TrimSpace(mentionUserID)
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}

	const (
		maxMessagesQueued = 3
		maxChars          = 1000
	)

	if len(line) > maxChars {
		line = line[:maxChars]
	}

	n.pingMu.Lock()
	defer n.pingMu.Unlock()

	// Try to append to the last queued message if it fits (group per user so we
	// only mention each user once per message).
	if len(n.pingQueue) > 0 {
		lastIdx := len(n.pingQueue) - 1
		last := n.pingQueue[lastIdx]
		if last.MentionEveryone != mentionEveryone {
			goto startNew
		}

		if mentionUserID == "" {
			last.Notices = append(last.Notices, line)
		} else {
			if last.LinesByUser == nil {
				last.LinesByUser = make(map[string][]string, 8)
			}
			if _, ok := last.LinesByUser[mentionUserID]; !ok {
				last.UserOrder = append(last.UserOrder, mentionUserID)
			}
			last.LinesByUser[mentionUserID] = append(last.LinesByUser[mentionUserID], line)
		}

		if rendered, _, _ := renderQueuedMessage(last); len(rendered) <= maxChars {
			n.pingQueue[lastIdx] = last
			return
		}
	}

startNew:
	// Start a new message if we still have capacity; otherwise drop.
	if len(n.pingQueue) >= maxMessagesQueued {
		n.droppedQueuedLines++
		return
	}
	msg := queuedDiscordMessage{}
	msg.MentionEveryone = mentionEveryone
	if mentionUserID == "" {
		msg.Notices = []string{line}
	} else {
		msg.UserOrder = []string{mentionUserID}
		msg.LinesByUser = map[string][]string{mentionUserID: {line}}
	}
	n.pingQueue = append(n.pingQueue, msg)
}

func renderQueuedMessage(m queuedDiscordMessage) (content string, mentions []string, allowEveryone bool) {
	lines := make([]string, 0, len(m.Notices)+len(m.UserOrder))
	for _, n := range m.Notices {
		n = strings.TrimSpace(n)
		if n != "" {
			lines = append(lines, n)
		}
	}
	if m.LinesByUser != nil {
		for _, uid := range m.UserOrder {
			uid = strings.TrimSpace(uid)
			if uid == "" {
				continue
			}
			parts := m.LinesByUser[uid]
			clean := make([]string, 0, len(parts))
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p != "" {
					clean = append(clean, p)
				}
			}
			if len(clean) == 0 {
				continue
			}
			lines = append(lines, fmt.Sprintf("<@%s> %s", uid, strings.Join(clean, " | ")))
			mentions = append(mentions, uid)
		}
	}
	allowEveryone = m.MentionEveryone
	if allowEveryone {
		if len(lines) == 0 {
			lines = append(lines, "@everyone")
		} else {
			lines[0] = "@everyone " + lines[0]
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), mentions, allowEveryone
}

func (n *discordNotifier) subscribedUserCount() int {
	if n == nil {
		return 0
	}
	n.stateMu.Lock()
	cached := len(n.links)
	n.stateMu.Unlock()
	if cached > 0 {
		return cached
	}
	if n.s == nil || n.s.workerLists == nil {
		return 0
	}
	if links, err := n.s.workerLists.ListEnabledDiscordLinks(); err == nil {
		return len(links)
	}
	return 0
}

func (n *discordNotifier) pingLoop(ctx context.Context) {
	// 1 message per 10 seconds max.
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n.sendNextQueuedMessage()
		}
	}
}

func (n *discordNotifier) sendNextQueuedMessage() {
	if n == nil || n.dg == nil {
		return
	}
	if !n.isNetworkOK() {
		return
	}
	channelID := strings.TrimSpace(n.notifyChannelID)
	if channelID == "" {
		return
	}

	n.pingMu.Lock()
	if len(n.pingQueue) == 0 {
		n.pingMu.Unlock()
		return
	}
	queued := append([]queuedDiscordMessage(nil), n.pingQueue...)
	n.pingMu.Unlock()

	// Outage guard: if we'd ping "too many" subscribed users at once, treat it as a
	// localized outage (e.g. upstream connectivity) and drop the notification burst.
	// Threshold: > max(100 users, 10% of subscribed users).
	uniqueUsers := make(map[string]struct{}, 256)
	for _, m := range queued {
		for _, id := range m.UserOrder {
			id = strings.TrimSpace(id)
			if id != "" {
				uniqueUsers[id] = struct{}{}
			}
		}
	}
	affected := len(uniqueUsers)
	subscribed := n.subscribedUserCount()
	if subscribed > 0 {
		pctThreshold := max(int(math.Ceil(float64(subscribed)*0.10)), 0)
		threshold := max(pctThreshold, 100)
		if affected > threshold {
			logger.Warn("notification burst dropped (possible localized outage)",
				"affected_users", affected,
				"subscribed_users", subscribed,
				"threshold", threshold,
			)
			n.resetAllNotificationState(time.Now())
			n.enqueueNotice(fmt.Sprintf(
				"Notification burst suppressed (possible localized outage): %d users (of %d) exceeded threshold %d. Notifications paused and state reset.",
				affected, subscribed, threshold,
			))
			return
		}
	}

	// Peek the next message; only pop it after a successful send.
	n.pingMu.Lock()
	if len(n.pingQueue) == 0 {
		n.pingMu.Unlock()
		return
	}
	next := n.pingQueue[0]
	n.pingMu.Unlock()
	msg, mentions, allowEveryone := renderQueuedMessage(next)

	if msg == "" {
		n.pingMu.Lock()
		if len(n.pingQueue) > 0 {
			n.pingQueue = n.pingQueue[1:]
		}
		n.pingMu.Unlock()
		return
	}

	if url := n.savedWorkersURL(); url != "" {
		footer := "[[check status]](" + url + ")"
		if len(msg)+2+len(footer) <= 1000 {
			msg = msg + "\n" + footer
		}
	}

	allowed := &discordgo.MessageAllowedMentions{
		Users: mentions,
	}
	if allowEveryone {
		allowed.Parse = []discordgo.AllowedMentionType{discordgo.AllowedMentionTypeEveryone}
	}

	_, err := n.dg.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content:         msg,
		AllowedMentions: allowed,
	})
	if err != nil {
		logger.Warn("discord notify send failed", "error", err)
		if isDiscordPermanentError(err) {
			n.pingMu.Lock()
			if len(n.pingQueue) > 0 {
				n.pingQueue = n.pingQueue[1:]
			}
			n.pingMu.Unlock()
		}
		return
	}

	prefix := n.noticePrefix()

	n.pingMu.Lock()
	if len(n.pingQueue) > 0 {
		n.pingQueue = n.pingQueue[1:]
	}
	if n.droppedQueuedLines > 0 {
		now := time.Now()
		if n.lastDropNoticeAt.IsZero() || now.Sub(n.lastDropNoticeAt) >= time.Minute {
			dropped := n.droppedQueuedLines
			n.droppedQueuedLines = 0
			n.lastDropNoticeAt = now
			// Best-effort: only enqueue if there's capacity.
			if len(n.pingQueue) < 3 {
				n.pingQueue = append(n.pingQueue, queuedDiscordMessage{
					Notices: []string{prefix + fmt.Sprintf("Notification backlog full; dropped %d updates to stay within rate limits.", dropped)},
				})
			}
		}
	}
	n.pingMu.Unlock()
}

func isDiscordPermanentError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, discordgo.ErrUnauthorized) {
		return true
	}
	var restErr *discordgo.RESTError
	if errors.As(err, &restErr) && restErr.Response != nil {
		switch restErr.Response.StatusCode {
		case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
			return true
		}
	}
	return false
}
