package api

import (
	"errors"
	"net/http"
	"strings"

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
		w.Header().Set("Idempotent-Replayed", "true")
		writeJSON(w, http.StatusOK, claim)
	case err == nil:
		writeJSON(w, http.StatusCreated, claim)
	case errors.Is(err, store.ErrSoldOut):
		writeError(w, http.StatusConflict, "SOLD_OUT", "not enough remaining in this drop")
	case errors.Is(err, store.ErrAlreadyClaimed):
		writeError(w, http.StatusConflict, "ALREADY_CLAIMED", "this buyer already has an active claim on this drop")
	case errors.Is(err, store.ErrNotOpen):
		writeError(w, http.StatusUnprocessableEntity, "DROP_NOT_OPEN", "this drop is not open right now")
	case errors.Is(err, store.ErrQtyExceedsLimit):
		writeError(w, http.StatusUnprocessableEntity, "QTY_EXCEEDS_LIMIT", "qty is above the per-buyer limit for this drop")
	case errors.Is(err, store.ErrIdempotencyMismatch):
		writeError(w, http.StatusUnprocessableEntity, "IDEMPOTENCY_KEY_REUSED", "this Idempotency-Key was already used with a different request")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "drop not found")
	default:
		s.internalError(w, r, err)
	}
}
