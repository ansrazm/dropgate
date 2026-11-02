package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ansrazm/dropgate/internal/store"
)

type createDropReq struct {
	Title       string    `json:"title"`
	TotalQty    int       `json:"total_qty"`
	PriceIDR    int64     `json:"price_idr"`
	MaxPerBuyer int       `json:"max_per_buyer"`
	HoldSeconds int       `json:"hold_seconds"`
	OpensAt     time.Time `json:"opens_at"`
	ClosesAt    time.Time `json:"closes_at"`
}

func (s *Server) createDrop(w http.ResponseWriter, r *http.Request) {
	var req createDropReq
	if !decode(w, r, &req) {
		return
	}
	if req.MaxPerBuyer == 0 {
		req.MaxPerBuyer = 1
	}
	if req.HoldSeconds == 0 {
		req.HoldSeconds = 600
	}

	req.Title = strings.TrimSpace(req.Title)
	switch {
	case req.Title == "" || len(req.Title) > 200:
		writeError(w, http.StatusUnprocessableEntity, "VALIDATION", "title must be 1-200 characters")
	case req.TotalQty < 1 || req.TotalQty > 1_000_000:
		writeError(w, http.StatusUnprocessableEntity, "VALIDATION", "total_qty must be between 1 and 1,000,000")
	case req.PriceIDR < 0:
		writeError(w, http.StatusUnprocessableEntity, "VALIDATION", "price_idr must be >= 0")
	case req.MaxPerBuyer < 1 || req.MaxPerBuyer > req.TotalQty:
		writeError(w, http.StatusUnprocessableEntity, "VALIDATION", "max_per_buyer must be between 1 and total_qty")
	case req.HoldSeconds < 30 || req.HoldSeconds > 86400:
		writeError(w, http.StatusUnprocessableEntity, "VALIDATION", "hold_seconds must be between 30 and 86400")
	case req.OpensAt.IsZero() || req.ClosesAt.IsZero() || !req.ClosesAt.After(req.OpensAt):
		writeError(w, http.StatusUnprocessableEntity, "VALIDATION", "opens_at and closes_at are required and closes_at must be after opens_at")
	default:
		d, err := s.store.CreateDrop(r.Context(), store.CreateDropInput{
			Title: req.Title, TotalQty: req.TotalQty, PriceIDR: req.PriceIDR,
			MaxPerBuyer: req.MaxPerBuyer, HoldSeconds: req.HoldSeconds,
			OpensAt: req.OpensAt, ClosesAt: req.ClosesAt,
		})
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, d)
	}
}

func (s *Server) getDrop(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	d, err := s.store.GetDrop(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "drop not found")
	case err != nil:
		s.internalError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, d)
	}
}

func (s *Server) listDrops(w http.ResponseWriter, r *http.Request) {
	drops, err := s.store.ListDrops(r.Context(), 50)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, drops)
}
