package engine

import "github.com/normahq/balda/pkg/actorlayer"

type ActorAddress = actorlayer.ActorAddress
type Envelope = actorlayer.Envelope

func SystemAddress(key string) ActorAddress {
	return actorlayer.SystemAddress(key)
}

func WildcardAddress(target string) string {
	return actorlayer.WildcardAddress(target)
}

func EncodeEnvelope(e Envelope) (string, error) {
	return actorlayer.EncodeEnvelope(e)
}

func DecodeEnvelope(raw string) (Envelope, error) {
	return actorlayer.DecodeEnvelope(raw)
}
