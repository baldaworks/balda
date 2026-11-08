package browserauth

import "context"

type sessionClientKey struct{}

type sessionClient struct {
	deviceLabel    string
	connectionPeer string
}

// WithSessionClient records bounded client metadata supplied by the trusted edge.
func WithSessionClient(ctx context.Context, deviceLabel, connectionPeer string) context.Context {
	return context.WithValue(ctx, sessionClientKey{}, sessionClient{
		deviceLabel:    deviceLabel,
		connectionPeer: connectionPeer,
	})
}

func sessionClientFromContext(ctx context.Context) (string, string) {
	client, _ := ctx.Value(sessionClientKey{}).(sessionClient)
	return client.deviceLabel, client.connectionPeer
}
