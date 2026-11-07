package control

import (
	"context"

	"github.com/jhzhfree/ai-governance-gateway/internal/identity"
	"github.com/jhzhfree/ai-governance-gateway/internal/newapibridge"
)

func (s *Store) WithNewAPIChannels(m *newapibridge.ChannelManager) *Store {
	s.channelManager = m
	return s
}
func (s *Store) NewAPIChannels(ctx context.Context, p identity.Principal, after string) (newapibridge.ChannelPage, error) {
	if s.channelManager == nil {
		return newapibridge.ChannelPage{}, identity.ErrForbidden
	}
	return s.channelManager.Channels(ctx, p, after)
}
func (s *Store) WriteNewAPIChannel(ctx context.Context, p identity.Principal, id int, in newapibridge.ChannelWrite) (newapibridge.ChannelOperation, error) {
	if s.channelManager == nil {
		return newapibridge.ChannelOperation{}, identity.ErrForbidden
	}
	return s.channelManager.WriteChannel(ctx, p, id, in)
}
func (s *Store) NewAPIChannelOperations(ctx context.Context, p identity.Principal) ([]newapibridge.ChannelOperation, error) {
	if s.channelManager == nil {
		return nil, identity.ErrForbidden
	}
	return s.channelManager.Operations(ctx, p)
}
func (s *Store) RecoverNewAPIChannelOperation(ctx context.Context, p identity.Principal, id string) (newapibridge.ChannelOperation, error) {
	if s.channelManager == nil {
		return newapibridge.ChannelOperation{}, identity.ErrForbidden
	}
	return s.channelManager.Operation(ctx, p, id, true)
}
