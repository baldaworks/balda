package session

import "errors"

// ErrWorkspaceCollision marks workspace path/branch collisions.
var ErrWorkspaceCollision = errors.New("workspace collision")
