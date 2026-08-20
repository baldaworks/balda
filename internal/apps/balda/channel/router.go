package channel

import (
	"context"
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
)

// Router routes outbound channel operations to the correct ChannelAdapter
// based on the locator's ChannelType.
type Router struct {
	adapters map[string]deliverycmd.Adapter
}

func NewRouter(adapters map[string]deliverycmd.Adapter) *Router { return &Router{adapters: adapters} }

func (r *Router) adapterFor(locator deliverycmd.Locator) (deliverycmd.Adapter, error) {
	adapter, ok := r.adapters[locator.ChannelType]
	if !ok {
		return nil, fmt.Errorf("no channel adapter for channel type %q", locator.ChannelType)
	}
	return adapter, nil
}

// Deliver routes one typed transport operation and returns provider metadata.
func (r *Router) Deliver(ctx context.Context, locator deliverycmd.Locator, operation deliverycmd.Operation) (deliverycmd.Result, error) {
	adapter, err := r.adapterFor(locator)
	if err != nil {
		return deliverycmd.Result{}, err
	}
	return adapter.Deliver(ctx, locator, operation)
}

func (r *Router) SendPlain(ctx context.Context, locator deliverycmd.Locator, text string) error {
	adapter, err := r.adapterFor(locator)
	if err != nil {
		return err
	}
	_, err = adapter.Deliver(ctx, locator, deliverycmd.Operation{Kind: deliverycmd.OperationPlain, Text: text})
	return err
}

func (r *Router) SendMarkdown(ctx context.Context, locator deliverycmd.Locator, text string) error {
	return r.SendMarkdownWithFormat(ctx, locator, "", text)
}

func (r *Router) SendMarkdownWithFormat(ctx context.Context, locator deliverycmd.Locator, format deliveryfmt.DeliveryFormat, text string) error {
	adapter, err := r.adapterFor(locator)
	if err != nil {
		return err
	}
	_, err = adapter.Deliver(ctx, locator, deliverycmd.Operation{Kind: deliverycmd.OperationMarkdown, DeliveryFormat: format, Text: text})
	return err
}

func (r *Router) SendAgentReply(ctx context.Context, locator deliverycmd.Locator, text string) error {
	adapter, err := r.adapterFor(locator)
	if err != nil {
		return err
	}
	_, err = adapter.Deliver(ctx, locator, deliverycmd.Operation{Kind: deliverycmd.OperationAgentReply, Text: text})
	return err
}

func (r *Router) SendAgentReplyWithProviderMessageID(ctx context.Context, locator deliverycmd.Locator, text string) (string, error) {
	return r.SendAgentReplyWithProviderMessageIDAndFormat(ctx, locator, "", text)
}

func (r *Router) SendAgentReplyWithProviderMessageIDAndFormat(ctx context.Context, locator deliverycmd.Locator, format deliveryfmt.DeliveryFormat, text string) (string, error) {
	return r.SendAgentReplyWithQuestion(ctx, locator, format, text, nil)
}

func (r *Router) SendAgentReplyWithQuestion(ctx context.Context, locator deliverycmd.Locator, format deliveryfmt.DeliveryFormat, text string, question *deliverycmd.Question) (string, error) {
	adapter, err := r.adapterFor(locator)
	if err != nil {
		return "", err
	}
	result, err := adapter.Deliver(ctx, locator, deliverycmd.Operation{Kind: deliverycmd.OperationAgentReply, DeliveryFormat: format, Text: text, Question: question})
	return result.ProviderMessageID, err
}

func (r *Router) ClearQuestionControls(ctx context.Context, locator deliverycmd.Locator, messageID, handle string) error {
	return r.SettleQuestionControls(ctx, locator, messageID, handle, "")
}

func (r *Router) SettleQuestionControls(ctx context.Context, locator deliverycmd.Locator, messageID, handle, selectionText string) error {
	adapter, err := r.adapterFor(locator)
	if err != nil {
		return err
	}
	_, err = adapter.Deliver(ctx, locator, deliverycmd.Operation{Kind: deliverycmd.OperationClearQuestionControls, MessageID: messageID, Handle: handle, Text: selectionText})
	return err
}

func (r *Router) SendDraftPlain(ctx context.Context, locator deliverycmd.Locator, draftID int, text string) error {
	adapter, err := r.adapterFor(locator)
	if err != nil {
		return err
	}
	_, err = adapter.Deliver(ctx, locator, deliverycmd.Operation{Kind: deliverycmd.OperationDraft, DraftID: draftID, Text: text})
	return err
}

func (r *Router) SendTyping(ctx context.Context, locator deliverycmd.Locator) error {
	adapter, err := r.adapterFor(locator)
	if err != nil {
		return err
	}
	_, err = adapter.Deliver(ctx, locator, deliverycmd.Operation{Kind: deliverycmd.OperationTyping})
	return err
}

func (r *Router) SendProgress(ctx context.Context, locator deliverycmd.Locator, progress deliverycmd.Progress) error {
	adapter, err := r.adapterFor(locator)
	if err != nil {
		return err
	}
	_, err = adapter.Deliver(ctx, locator, deliverycmd.Operation{Kind: deliverycmd.OperationProgress, Progress: progress})
	return err
}

func (r *Router) SendPhoto(ctx context.Context, locator deliverycmd.Locator, fileID, caption string) error {
	return r.SendPhotoMedia(ctx, locator, deliverycmd.Media{
		Kind:    "photo",
		FileID:  fileID,
		Caption: caption,
	})
}

func (r *Router) SendPhotoMedia(ctx context.Context, locator deliverycmd.Locator, media deliverycmd.Media) error {
	adapter, err := r.adapterFor(locator)
	if err != nil {
		return err
	}
	_, err = adapter.Deliver(ctx, locator, deliverycmd.Operation{
		Kind:  deliverycmd.OperationPhoto,
		Media: &media,
	})
	return err
}

func (r *Router) SendDocument(ctx context.Context, locator deliverycmd.Locator, fileID, caption, name string) error {
	return r.SendDocumentMedia(ctx, locator, deliverycmd.Media{
		Kind:    "document",
		FileID:  fileID,
		Caption: caption,
		Name:    name,
	})
}

func (r *Router) SendDocumentMedia(ctx context.Context, locator deliverycmd.Locator, media deliverycmd.Media) error {
	adapter, err := r.adapterFor(locator)
	if err != nil {
		return err
	}
	_, err = adapter.Deliver(ctx, locator, deliverycmd.Operation{
		Kind:  deliverycmd.OperationDocument,
		Media: &media,
	})
	return err
}
