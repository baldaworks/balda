package engine

import "github.com/normahq/balda/pkg/actorlayer"

type ActorAddress = actorlayer.ActorAddress
type Envelope = actorlayer.Envelope

// SystemAddress returns an actorlayer system address.
func SystemAddress(key string) ActorAddress {
	return actorlayer.SystemAddress(key)
}

// WildcardAddress returns the normalized wildcard address for a target.
func WildcardAddress(target string) string {
	return actorlayer.WildcardAddress(target)
}

// EncodeEnvelope validates and marshals an envelope as JSON.
func EncodeEnvelope(e Envelope) (string, error) {
	return actorlayer.EncodeEnvelope(e)
}

// DecodeEnvelope unmarshals and validates an envelope JSON string.
func DecodeEnvelope(raw string) (Envelope, error) {
	return actorlayer.DecodeEnvelope(raw)
}
