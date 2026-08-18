package api

import (
	"context"
	"net/http"
	"time"

	"github.com/trentkm/stormlight/internal/history"
)

func (s *Server) listHistory(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := call(r)
	defer cancel()
	records, err := s.service.SessionHistory(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, orEmpty(records))
}

func (s *Server) resumeHistory(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	listCtx, cancel := call(r)
	defer cancel()
	records, err := s.service.SessionHistory(listCtx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	var record history.Record
	for _, candidate := range records {
		if candidate.SessionID == sessionID {
			record = candidate
			break
		}
	}
	if record.SessionID == "" {
		writeError(w, http.StatusNotFound, "no such conversation: "+sessionID)
		return
	}
	// Resuming starts a provider process, the same wait dispatching has.
	resumeCtx, cancelResume := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancelResume()
	resumed, err := s.service.Resume(resumeCtx, record)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, agentView{Agent: resumed, Host: resumed.Host})
}
