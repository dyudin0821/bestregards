package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/dyudin0821/bestregards/internal/events"
	"github.com/dyudin0821/bestregards/internal/translate"
	"github.com/dyudin0821/bestregards/internal/user"
)

const (
	maxClientEvents = 10
	maxEventsBody   = 8 << 10
)

type clientEvent struct {
	Type     string `json:"type"`
	Kind     string `json:"kind"`
	Mode     string `json:"mode"`
	Level    int    `json:"level"`
	ResultID string `json:"resultId"`
}

// clientEvents takes events only the browser can know about: that a ready-made pair was shown and
// that a result was copied. Everything else is recorded by the server. Anonymous visitors are
// allowed; a valid session only adds the user and the campaign. These events can be forged, which
// is acceptable for an internal usage metric.
func (s *Server) clientEvents(w http.ResponseWriter, r *http.Request) {
	if !s.allow(s.evLimiter, r) {
		w.Header().Set("Retry-After", "60")
		writeCoded(w, http.StatusTooManyRequests, "rate_limited", "Слишком много запросов.", nil)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxEventsBody)
	var body struct {
		Events []clientEvent `json:"events"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Events) == 0 || len(body.Events) > maxClientEvents {
		writeCoded(w, http.StatusBadRequest, "invalid", "Ожидается от 1 до 10 событий.", nil)
		return
	}

	var u user.User
	source := events.SourceWeb
	if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		if authed, err := s.Auth.Authenticate(r.Context(), token); err == nil {
			u, source = authed, channelOf(authed)
		}
	}

	out := make([]events.Event, 0, len(body.Events))
	for _, e := range body.Events {
		ev, ok := s.validClientEvent(e)
		if !ok {
			writeCoded(w, http.StatusBadRequest, "invalid", "Недопустимое событие.", nil)
			return
		}
		ev.Source, ev.Campaign, ev.UserID = source, u.Source, u.ID
		out = append(out, ev)
	}
	for _, ev := range out {
		s.Events.Record(ev)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) validClientEvent(e clientEvent) (events.Event, bool) {
	t, kind := events.Type(e.Type), events.Kind(e.Kind)
	if !events.ClientTypes[t] || !events.ValidResultID(e.ResultID) || e.Level < 0 || e.Level > translate.MaxLevel {
		return events.Event{}, false
	}
	if kind != events.KindPair && kind != events.KindAI {
		return events.Event{}, false
	}
	// AI translations are recorded by the server; accepting them here would count them twice.
	if t == events.TranslateDone && kind != events.KindPair {
		return events.Event{}, false
	}
	known := false
	for _, m := range s.Catalog.Modes {
		known = known || m.ID == e.Mode
	}
	if !known {
		return events.Event{}, false
	}
	return events.Event{Type: t, Kind: kind, Mode: e.Mode, Level: e.Level, ResultID: e.ResultID}, true
}
