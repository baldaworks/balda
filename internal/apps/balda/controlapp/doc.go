// Package controlapp owns application-level control services for Balda jobs and sessions.
//
// It coordinates cancel/clear/schedule control use-cases over durable job state and
// turn-queue collaborators. It does not own actor transport parsing or runtime policy.
package controlapp
