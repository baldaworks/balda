package managementhttp

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/accessapp"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (s strictServer) ListBindingChannels(context.Context, managementv1.ListBindingChannelsRequestObject) (managementv1.ListBindingChannelsResponseObject, error) {
	channels := s.channels.List()
	result := managementv1.BindingChannelInventory{Channels: make([]managementv1.BindingChannel, 0, len(channels))}
	for _, channel := range channels {
		result.Channels = append(result.Channels, bindingChannelDTO(channel))
	}
	return managementv1.ListBindingChannels200JSONResponse(result), nil
}

func (s strictServer) RefreshBindingChannel(ctx context.Context, request managementv1.RefreshBindingChannelRequestObject) (managementv1.RefreshBindingChannelResponseObject, error) {
	channel, err := s.channels.Refresh(ctx, request.ChannelType)
	if err != nil {
		return nil, err
	}
	return managementv1.RefreshBindingChannel200JSONResponse(bindingChannelDTO(channel)), nil
}

func (s strictServer) ListUserBindings(ctx context.Context, request managementv1.ListUserBindingsRequestObject) (managementv1.ListUserBindingsResponseObject, error) {
	user, err := s.access.GetUser(ctx, accessActor(ctx), request.UserID)
	if err != nil {
		return nil, err
	}
	invitations, err := s.invitations.Pending(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	result := managementv1.UserBindingInventory{
		Bindings:    make([]managementv1.BindingRecord, 0, len(user.Bindings)),
		Invitations: make([]managementv1.BindingInvitation, 0, len(invitations)),
	}
	for _, binding := range user.Bindings {
		result.Bindings = append(result.Bindings, bindingDTO(binding))
	}
	for _, invitation := range invitations {
		result.Invitations = append(result.Invitations, bindingInvitationDTO(invitation))
	}
	return managementv1.ListUserBindings200JSONResponse(result), nil
}

func (s strictServer) DeleteUserBinding(ctx context.Context, request managementv1.DeleteUserBindingRequestObject) (managementv1.DeleteUserBindingResponseObject, error) {
	if request.Body == nil || !bool(request.Body.ConfirmImpact) {
		return nil, usercmd.ErrInvalid
	}
	err := s.access.RemoveBinding(ctx, accessActor(ctx), accessapp.BindingRemoval{
		UserID: request.UserID, BindingID: request.BindingID,
		ExpectedVersion: request.Body.ExpectedUserVersion, ConfirmImpact: true,
	})
	if err != nil {
		return nil, err
	}
	return managementv1.DeleteUserBinding204Response{}, nil
}

func (s strictServer) IssueBindingInvitation(ctx context.Context, request managementv1.IssueBindingInvitationRequestObject) (managementv1.IssueBindingInvitationResponseObject, error) {
	if request.Body == nil {
		return nil, usercmd.ErrInvalid
	}
	if _, err := s.access.GetUser(ctx, accessActor(ctx), request.UserID); err != nil {
		return nil, err
	}
	channel, ok := s.channels.Get(request.Body.ChannelType)
	if !ok || channel.Integration.Key == "" {
		return nil, usercmd.ErrBindingInvitationUnavailable
	}
	principal := webAuthFromContext(ctx).principal
	issued, err := s.invitations.Issue(ctx, usercmd.InvitationActor{
		UserID: principal.User.ID, WebAuthSessionID: principal.WebAuthSessionID,
	}, request.UserID, request.Body.ExpectedUserVersion, channel.Integration, request.Body.Replace)
	if err != nil {
		return nil, err
	}
	return managementv1.IssueBindingInvitation200JSONResponse{
		Invitation: bindingInvitationDTO(issued.Invitation), Proof: issued.Payload,
	}, nil
}

func (s strictServer) CancelBindingInvitation(ctx context.Context, request managementv1.CancelBindingInvitationRequestObject) (managementv1.CancelBindingInvitationResponseObject, error) {
	if request.Body == nil || !bool(request.Body.Confirm) {
		return nil, usercmd.ErrInvalid
	}
	if _, err := s.access.GetUser(ctx, accessActor(ctx), request.UserID); err != nil {
		return nil, err
	}
	principal := webAuthFromContext(ctx).principal
	err := s.invitations.Cancel(ctx, usercmd.InvitationActor{
		UserID: principal.User.ID, WebAuthSessionID: principal.WebAuthSessionID,
	}, request.UserID, request.InvitationID, request.Body.ExpectedInvitationVersion)
	if err != nil {
		return nil, err
	}
	return managementv1.CancelBindingInvitation204Response{}, nil
}

func bindingDTO(binding usercmd.Binding) managementv1.BindingRecord {
	result := managementv1.BindingRecord{
		BindingID: binding.ID, ChannelType: binding.ChannelType, Principal: binding.Principal,
		CreatedAt: binding.CreatedAt.UTC(), UpdatedAt: binding.UpdatedAt.UTC(),
	}
	if binding.DisplayName != "" {
		result.DisplayName = &binding.DisplayName
	}
	if binding.ProviderUsername != "" {
		result.ProviderUsername = &binding.ProviderUsername
	}
	if binding.ProviderFirstName != "" {
		result.ProviderFirstName = &binding.ProviderFirstName
	}
	return result
}

func bindingInvitationDTO(invitation usercmd.BindingInvitation) managementv1.BindingInvitation {
	result := managementv1.BindingInvitation{
		InvitationID: invitation.ID, ChannelType: invitation.Integration.ChannelType,
		IssuedByUserID: invitation.IssuedBy, CreatedAt: invitation.CreatedAt.UTC(),
		ExpiresAt: invitation.ExpiresAt.UTC(), Version: invitation.Version,
	}
	if !invitation.ConsumedAt.IsZero() {
		value := invitation.ConsumedAt.UTC()
		result.ConsumedAt = &value
	}
	if !invitation.RevokedAt.IsZero() {
		value := invitation.RevokedAt.UTC()
		result.RevokedAt = &value
	}
	return result
}

func bindingChannelDTO(channel usercmd.BindingChannel) managementv1.BindingChannel {
	result := managementv1.BindingChannel{
		ChannelType: channel.Integration.ChannelType, Ready: channel.Integration.Key != "",
		CommandsEnabled: channel.CommandsEnabled,
	}
	if channel.Name != "" {
		result.Name = &channel.Name
	}
	if channel.BotUsername != "" {
		result.BotUsername = &channel.BotUsername
	}
	if channel.Endpoint != "" {
		result.Endpoint = &channel.Endpoint
	}
	return result
}
