package service

import "context"

// WithContext returns a request-scoped provider. Token and fallback state remain
// shared with the original provider; only the operation context is isolated.
func (s *MoviePilotService) WithContext(ctx context.Context) *MoviePilotService {
	if s == nil {
		return nil
	}
	scoped := NewMoviePilotService(s.cfg, s.logger)
	scoped.client, scoped.local, scoped.operationCtx = s.client, s.local, ctx
	scoped.stateOwner = s.sharedState()
	return scoped
}

func (s *MoviePilotService) requestContext() context.Context {
	if s != nil && s.operationCtx != nil {
		return s.operationCtx
	}
	return context.Background()
}

func (s *MoviePilotService) sharedState() *MoviePilotService {
	if s.stateOwner != nil {
		return s.stateOwner
	}
	return s
}
