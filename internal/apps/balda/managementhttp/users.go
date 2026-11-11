package managementhttp

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/accessapp"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (s strictServer) ListUsers(ctx context.Context, _ managementv1.ListUsersRequestObject) (managementv1.ListUsersResponseObject, error) {
	users, err := s.access.ListUsers(ctx, accessActor(ctx))
	if err != nil {
		return nil, err
	}
	result := managementv1.UserInventory{Users: make([]managementv1.UserRecord, 0, len(users))}
	for _, user := range users {
		result.Users = append(result.Users, userDTO(user))
	}
	return managementv1.ListUsers200JSONResponse(result), nil
}

func (s strictServer) GetUser(ctx context.Context, request managementv1.GetUserRequestObject) (managementv1.GetUserResponseObject, error) {
	user, err := s.access.GetUser(ctx, accessActor(ctx), request.UserID)
	if err != nil {
		return nil, err
	}
	return managementv1.GetUser200JSONResponse(userDTO(user)), nil
}

func (s strictServer) CreateUser(ctx context.Context, request managementv1.CreateUserRequestObject) (managementv1.CreateUserResponseObject, error) {
	if request.Body == nil || request.Body.TemporaryPassword == nil {
		return nil, usercmd.ErrInvalid
	}
	password := []byte(*request.Body.TemporaryPassword)
	defer zero(password)
	user, err := s.access.CreateUser(ctx, accessActor(ctx), accessapp.CreateInput{
		DisplayName: request.Body.DisplayName, Username: request.Body.Username, TemporaryPassword: password,
		Role: usercmd.Role(request.Body.Role), Status: usercmd.UserStatus(request.Body.Status),
	})
	if err != nil {
		return nil, err
	}
	return managementv1.CreateUser200JSONResponse(userDTO(user)), nil
}

func (s strictServer) UpdateUser(ctx context.Context, request managementv1.UpdateUserRequestObject) (managementv1.UpdateUserResponseObject, error) {
	if request.Body == nil {
		return nil, usercmd.ErrInvalid
	}
	user, err := s.access.UpdateUser(ctx, accessActor(ctx), accessapp.UpdateInput{
		UserID: request.UserID, DisplayName: request.Body.DisplayName, Username: request.Body.Username,
		Role: usercmd.Role(request.Body.Role), Status: usercmd.UserStatus(request.Body.Status),
		ExpectedVersion: request.Body.ExpectedVersion, AcknowledgeBotAccessImpact: request.Body.AcknowledgeBotAccessImpact,
	})
	if err != nil {
		return nil, err
	}
	return managementv1.UpdateUser200JSONResponse(userDTO(user)), nil
}

func (s strictServer) ResetUserCredential(ctx context.Context, request managementv1.ResetUserCredentialRequestObject) (managementv1.ResetUserCredentialResponseObject, error) {
	if request.Body == nil || request.Body.TemporaryPassword == nil {
		return nil, usercmd.ErrInvalid
	}
	password := []byte(*request.Body.TemporaryPassword)
	defer zero(password)
	err := s.access.ResetCredential(ctx, accessActor(ctx), accessapp.CredentialInput{
		UserID: request.UserID, TemporaryPassword: password, ExpectedUserVersion: request.Body.ExpectedUserVersion,
		ExpectedCredentialVersion: request.Body.ExpectedCredentialVersion, ConfirmCurrent: request.Body.ConfirmCurrent,
	})
	if err != nil {
		return nil, err
	}
	return managementv1.ResetUserCredential204Response{}, nil
}

func accessActor(ctx context.Context) accessapp.Actor {
	principal := webAuthFromContext(ctx).principal
	return accessapp.Actor{User: principal.User, WebAuthSessionID: principal.WebAuthSessionID}
}

func userDTO(user usercmd.User) managementv1.UserRecord {
	return managementv1.UserRecord{
		UserID: user.ID, DisplayName: user.DisplayName, Username: user.Username,
		Status: managementv1.UserStatus(user.Status), Role: managementv1.WebAuthRole(user.Role),
		CredentialState:      managementv1.UserCredentialState(user.Credential.State),
		CredentialMustChange: user.Credential.MustChange, CredentialVersion: user.Credential.Version,
		Primary: user.Primary, Version: user.Version, CreatedAt: user.CreatedAt.UTC(), UpdatedAt: user.UpdatedAt.UTC(),
	}
}
