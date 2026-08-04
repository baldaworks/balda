package telegram

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/normahq/balda/internal/apps/balda/attachment"
	"github.com/tgbotkit/client"
)

const (
	defaultMediaGroupFlushDelay = 500 * time.Millisecond
	mediaGroupDispatchTimeout   = 30 * time.Second
)

type mediaGroupKey struct {
	chatID       int64
	topicID      int
	userID       int64
	mediaGroupID string
}

type pendingMediaGroup struct {
	messages   []MessageContext
	dispatch   func(context.Context, MessageContext)
	generation uint64
	timer      *time.Timer
}

type mediaGroupCollector struct {
	mu     sync.Mutex
	delay  time.Duration
	groups map[mediaGroupKey]*pendingMediaGroup
}

func newMediaGroupCollector(delay time.Duration) *mediaGroupCollector {
	return &mediaGroupCollector{
		delay:  delay,
		groups: make(map[mediaGroupKey]*pendingMediaGroup),
	}
}

func (c *mediaGroupCollector) collect(
	message MessageContext,
	dispatch func(context.Context, MessageContext),
) bool {
	mediaGroupID := strings.TrimSpace(message.MediaGroupID)
	if c == nil || dispatch == nil {
		return false
	}
	if mediaGroupID == "" {
		c.flushMatching(func(key mediaGroupKey) bool {
			return key.sameBoundary(message)
		})
		return false
	}

	key := mediaGroupKey{
		chatID:       message.ChatID,
		topicID:      message.TopicID,
		userID:       message.UserID,
		mediaGroupID: mediaGroupID,
	}
	c.flushMatching(func(existing mediaGroupKey) bool {
		return existing != key && existing.sameBoundary(message)
	})

	c.mu.Lock()
	pending := c.groups[key]
	if pending == nil {
		pending = &pendingMediaGroup{dispatch: dispatch}
		c.groups[key] = pending
	}
	pending.messages = append(pending.messages, cloneMessageContext(message))
	pending.generation++
	generation := pending.generation
	if pending.timer != nil {
		pending.timer.Stop()
	}
	delay := c.delay
	pending.timer = time.AfterFunc(delay, func() {
		c.flush(key, generation)
	})
	c.mu.Unlock()
	return true
}

func (c *mediaGroupCollector) flush(key mediaGroupKey, generation uint64) {
	c.mu.Lock()
	pending := c.groups[key]
	if pending == nil || pending.generation != generation {
		c.mu.Unlock()
		return
	}
	delete(c.groups, key)
	messages := append([]MessageContext(nil), pending.messages...)
	c.mu.Unlock()
	c.dispatch(pending, messages)
}

func (c *mediaGroupCollector) flushMatching(match func(mediaGroupKey) bool) {
	if c == nil || match == nil {
		return
	}
	c.mu.Lock()
	pending := make([]*pendingMediaGroup, 0)
	for key, group := range c.groups {
		if !match(key) {
			continue
		}
		delete(c.groups, key)
		if group.timer != nil {
			group.timer.Stop()
		}
		pending = append(pending, group)
	}
	c.mu.Unlock()

	sort.SliceStable(pending, func(i, j int) bool {
		return firstMessageID(pending[i].messages) < firstMessageID(pending[j].messages)
	})
	for _, group := range pending {
		c.dispatch(group, append([]MessageContext(nil), group.messages...))
	}
}

func (*mediaGroupCollector) dispatch(pending *pendingMediaGroup, messages []MessageContext) {
	if pending == nil || pending.dispatch == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), mediaGroupDispatchTimeout)
	defer cancel()
	pending.dispatch(ctx, mergeMediaGroup(messages))
}

func (key mediaGroupKey) sameBoundary(message MessageContext) bool {
	return key.chatID == message.ChatID && key.topicID == message.TopicID && key.userID == message.UserID
}

func firstMessageID(messages []MessageContext) int {
	if len(messages) == 0 {
		return 0
	}
	first := messages[0].MessageID
	for _, message := range messages[1:] {
		if message.MessageID < first {
			first = message.MessageID
		}
	}
	return first
}

func mergeMediaGroup(messages []MessageContext) MessageContext {
	if len(messages) == 0 {
		return MessageContext{}
	}
	messages = append([]MessageContext(nil), messages...)
	sort.SliceStable(messages, func(i, j int) bool {
		return messages[i].MessageID < messages[j].MessageID
	})

	merged := cloneMessageContext(messages[0])
	merged.Attachments = nil
	for _, message := range messages {
		if strings.TrimSpace(merged.Text) == "" && strings.TrimSpace(message.Text) != "" {
			merged.Text = message.Text
		}
		merged.Attachments = append(merged.Attachments, message.Attachments...)
	}
	merged.Attachments = attachment.NormalizeList(merged.Attachments)
	return merged
}

func cloneMessageContext(message MessageContext) MessageContext {
	message.Entities = append([]client.MessageEntity(nil), message.Entities...)
	message.Attachments = append([]attachment.Descriptor(nil), message.Attachments...)
	return message
}
