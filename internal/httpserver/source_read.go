package httpserver

import (
	"context"

	"github.com/orpheus-agents/orpheus/internal/api"
	"github.com/orpheus-agents/orpheus/internal/servicecredentials"
	"github.com/orpheus-agents/orpheus/internal/session"
)

func (s *Server) CheckAssignmentSourceRead(ctx context.Context, request api.CheckAssignmentSourceReadRequestObject) (api.CheckAssignmentSourceReadResponseObject, error) {
	b := request.Body
	if b == nil {
		return nil, session.Problem(422, "invalid_request", "A source check is required.")
	}
	r := servicecredentials.SourceRead{Request: b.RequestID, Invocation: b.InvocationID,
		Assignment: b.AssignmentDigest, Job: b.JobID, Artifact: b.ArtifactSha256, Source: b.SourceID,
		Revision: b.SourceRevision, Purpose: string(b.Purpose), Signature: b.Signature}
	allowed, err := s.Store.CheckSourceRead(ctx, r)
	if err != nil {
		return nil, err
	}
	return api.CheckAssignmentSourceRead200JSONResponse{RequestID: r.Request, InvocationID: r.Invocation,
		AssignmentDigest: r.Assignment, MaterialSha256: r.Digest(), Allowed: allowed}, nil
}
