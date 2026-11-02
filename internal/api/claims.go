package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/ansrazm/dropgate/internal/events"
	"github.com/ansrazm/dropgate/internal/store"
)

type createClaimReq struct {
	BuyerRef string `json:"buyer_ref"`
	Qty      int    `json:"qty"`
}

func (s *Server) createClaim(w http.ResponseWriter, r *http.Request) {
	dropID, ok := pathID(w, r)
	if !ok {
		return
	}

	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 200 {
		writeError(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "send an Idempotency-Key header (1-200 characters)")
		return
	}

	var req createClaimReq
	if !decode(w, r, &req) {
		return
	}
	req.BuyerRef = strings.TrimSpace(req.BuyerRef)
	if req.BuyerRef == "" || len(req.BuyerRef) > 200 || req.Qty < 1 {
		writeError(w, http.StatusUnprocessableEntity, "VALIDATION", "buyer_ref (1-200 chars) and qty (>= 1) are required")
		return
	}

	claim, replayed, err := s.store.CreateClaim(r.Context(), store.ClaimInput{
		DropID: dropID, BuyerRef: req.BuyerRef, Qty: req.Qty, IdempotencyKey: key,
	})
	switch {
	case err == nil && replayed:
		s.emit(r, events.ClaimReplayed, dropID, req.BuyerRef, map[string]any{"claim_id": claim.ID, "qty": claim.Qty})
		w.Header().Set("Idempotent-Replayed", "true")
		writeJSON(w, http.StatusOK, claim)
	case err == nil:
		s.emit(r, events.ClaimCreated, dropID, req.BuyerRef, map[string]any{
			"claim_id": claim.ID, "qty": claim.Qty, "expires_at": claim.ExpiresAt.UTC().Format(time.RFC3339),
		})
		writeJSON(w, http.StatusCreated, claim)
	case errors.Is(err, store.ErrSoldOut):
		s.reject(r, dropID, req, "SOLD_OUT")
		writeError(w, http.StatusConflict, "SOLD_OUT", "not enough remaining in this drop")
	case errors.Is(err, store.ErrAlreadyClaimed):
		s.reject(r, dropID, req, "ALREADY_CLAIMED")
		writeError(w, http.StatusConflict, "ALREADY_CLAIMED", "this buyer already has an active claim on this drop")
	case errors.Is(err, store.ErrNotOpen):
		s.reject(r, dropID, req, "DROP_NOT_OPEN")
		writeError(w, http.StatusUnprocessableEntity, "DROP_NOT_OPEN", "this drop is not open right now")
	case errors.Is(err, store.ErrQtyExceedsLimit):
		s.reject(r, dropID, req, "QTY_EXCEEDS_LIMIT")
		writeError(w, http.StatusUnprocessableEntity, "QTY_EXCEEDS_LIMIT", "qty is above the per-buyer limit for this drop")
	case errors.Is(err, store.ErrIdempotencyMismatch):
		s.reject(r, dropID, req, "IDEMPOTENCY_KEY_REUSED")
		writeError(w, http.StatusUnprocessableEntity, "IDEMPOTENCY_KEY_REUSED", "this Idempotency-Key was already used with a different request")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "drop not found")
	default:
		s.internalError(w, r, err)
	}
}

// emit records an event after the database result is already decided. It is
// fire-and-forget: the response never depends on the event log.
func (s *Server) emit(r *http.Request, typ, dropID, buyer string, data map[string]any) {
	s.events.Record(events.Event{
		Type: typ, DropID: dropID, BuyerRef: buyer,
		RequestID: middleware.GetReqID(r.Context()), Data: data,
	})
}

func (s *Server) reject(r *http.Request, dropID string, req createClaimReq, reason string) {
	s.emit(r, events.ClaimRejected, dropID, req.BuyerRef, map[string]any{"reason": reason, "qty": req.Qty})
}
