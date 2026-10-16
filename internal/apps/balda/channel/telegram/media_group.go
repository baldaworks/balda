package telegram

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/attachment"
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
	mu        sync.Mutex
	delay     time.Duration
	groups    map[mediaGroupKey]*pendingMediaGroup
	ctx       context.Context
	cancel    context.CancelFunc
	callbacks sync.WaitGroup
	done      chan struct{}
	stopped   bool
}

func newMediaGroupCollector(delay time.Duration) *mediaGroupCollector {
	ctx, cancel := context.WithCancel(context.Background())
	return &mediaGroupCollector{
		delay:  delay,
		groups: make(map[mediaGroupKey]*pendingMediaGroup),
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
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
	if c.stopped {
		c.mu.Unlock()
		return true
	}
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
	if c.stopped || pending == nil || pending.generation != generation {
		c.mu.Unlock()
		return
	}
	delete(c.groups, key)
	messages := append([]MessageContext(nil), pending.messages...)
	c.callbacks.Add(1)
	c.mu.Unlock()
	c.dispatch(pending, messages)
}

func (c *mediaGroupCollector) flushMatching(match func(mediaGroupKey) bool) {
	if c == nil || match == nil {
		return
	}
	c.mu.Lock()
	pending := make([]*pendingMediaGroup, 0)
	if c.stopped {
		c.mu.Unlock()
		return
	}
	for key, group := range c.groups {
		if !match(key) {
			continue
		}
		delete(c.groups, key)
		if group.timer != nil {
			group.timer.Stop()
		}
		pending = append(pending, group)
		c.callbacks.Add(1)
	}
	c.mu.Unlock()

	sort.SliceStable(pending, func(i, j int) bool {
		return firstMessageID(pending[i].messages) < firstMessageID(pending[j].messages)
	})
	for _, group := range pending {
		c.dispatch(group, append([]MessageContext(nil), group.messages...))
	}
}

func (c *mediaGroupCollector) dispatch(pending *pendingMediaGroup, messages []MessageContext) {
	defer c.callbacks.Done()
	if pending == nil || pending.dispatch == nil {
		return
	}
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return
	}
	root := c.ctx
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(root, mediaGroupDispatchTimeout)
	defer cancel()
	pending.dispatch(ctx, mergeMediaGroup(messages))
}

func (c *mediaGroupCollector) stop(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	c.mu.Lock()
	if !c.stopped {
		c.stopped = true
		for _, group := range c.groups {
			if group.timer != nil {
				group.timer.Stop()
			}
		}
		c.groups = make(map[mediaGroupKey]*pendingMediaGroup)
		c.cancel()
		go func() {
			c.callbacks.Wait()
			close(c.done)
		}()
	}
	done := c.done
	c.mu.Unlock()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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
