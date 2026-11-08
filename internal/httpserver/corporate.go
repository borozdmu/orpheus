package httpserver

import (
	"context"

	"github.com/orpheus-agents/orpheus/internal/api"
	"github.com/orpheus-agents/orpheus/internal/session"
)

func (s *Server) ConfirmAgentAccess(ctx context.Context, request api.ConfirmAgentAccessRequestObject) (api.ConfirmAgentAccessResponseObject, error) {
	if request.Body == nil {
		return nil, session.Problem(422, "invalid_access_context", "Invocation is required.")
	}
	if err := s.Store.ConfirmInvocation(ctx, request.Body.InvocationID); err != nil {
		return nil, err
	}
	return api.ConfirmAgentAccess200JSONResponse{Invocation: request.Body.InvocationID}, nil
}
