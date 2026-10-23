package sdk115helper

import sdk115 "github.com/OpenListTeam/115-sdk-go"

// WithAccessToken isolates account credentials while reusing the HTTP client.
// In the pinned SDK v0.2.6, Client contains a Resty client pointer, credential
// strings and a refresh callback, with no mutex or other non-copyable state.
// Configure the base client before sharing it; do not mutate client-wide HTTP
// settings through a scoped copy.
func WithAccessToken(base *sdk115.Client, token string) *sdk115.Client {
	scoped := *base
	scoped.SetAccessToken(token)
	return &scoped
}
