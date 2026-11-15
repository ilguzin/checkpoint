package core

import (
	"context"
	"net/http"
	"time"

	"github.com/livekit/protocol/livekit"
)

func (s *Server) meetingRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/meeting", s.withSession(func(w http.ResponseWriter, r *http.Request, p Session) {
		out, e := s.store.MeetingState(p.Identity)
		if e != nil {
			accessError(w, e)
			return
		}
		s.presenceMu.RLock()
		members := out.Members[:0]
		for _, m := range out.Members {
			if !m.Admitted || m.Identity == p.Identity || s.presence[p.MediaRoom][m.Identity] {
				members = append(members, m)
			}
		}
		s.presenceMu.RUnlock()
		out.Members = members
		reply(w, 200, out)
	}))
	mux.HandleFunc("POST /api/hand", s.withSession(func(w http.ResponseWriter, r *http.Request, p Session) {
		var in struct{ Raised bool }
		if !decode(w, r, &in) {
			return
		}
		if e := s.store.Hand(p.Identity, in.Raised); e != nil {
			accessError(w, e)
			return
		}
		reply(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/messages", s.withSession(func(w http.ResponseWriter, r *http.Request, p Session) {
		var in struct{ Kind, Body string }
		if !decode(w, r, &in) {
			return
		}
		if e := s.store.SendMessage(p.Identity, in.Kind, in.Body); e != nil {
			accessError(w, e)
			return
		}
		reply(w, 201, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/questions/answer", s.withSession(func(w http.ResponseWriter, r *http.Request, p Session) {
		var in struct{ ID int64 }
		if !decode(w, r, &in) {
			return
		}
		if e := s.store.Answer(p.Identity, in.ID); e != nil {
			accessError(w, e)
			return
		}
		reply(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/meeting/policy", s.withSession(func(w http.ResponseWriter, r *http.Request, p Session) {
		var in struct{ Locked, WaitingRoom bool }
		if !decode(w, r, &in) {
			return
		}
		if e := s.store.MeetingPolicy(p.Identity, in.Locked, in.WaitingRoom); e != nil {
			accessError(w, e)
			return
		}
		reply(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/meeting/end", s.withSession(func(w http.ResponseWriter, r *http.Request, p Session) {
		s.permissions.Lock()
		defer s.permissions.Unlock()
		if e := s.store.EndMeeting(p.Identity); e != nil {
			accessError(w, e)
			return
		}
		s.wakeReconcile()
		reply(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/leave", s.withSession(func(w http.ResponseWriter, r *http.Request, p Session) {
		s.permissions.Lock()
		defer s.permissions.Unlock()
		if e := s.store.Leave(p.Identity); e != nil {
			accessError(w, e)
			return
		}
		s.wakeReconcile()
		reply(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/admit", s.withSession(func(w http.ResponseWriter, r *http.Request, p Session) {
		var in struct{ Identity string }
		if !decode(w, r, &in) {
			return
		}
		s.permissions.Lock()
		defer s.permissions.Unlock()
		if e := s.store.Moderate(p.Identity, in.Identity, "admit", false); e != nil {
			accessError(w, e)
			return
		}
		reply(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/screen", s.withSession(s.screen))
}
func (s *Server) screen(w http.ResponseWriter, r *http.Request, p Session) {
	var in struct{ Claim bool }
	if !decode(w, r, &in) {
		return
	}
	s.permissions.Lock()
	defer s.permissions.Unlock()
	if e := s.store.Screen(p.Identity, in.Claim); e != nil {
		fail(w, 409, "Screen sharing is unavailable or already in use")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	members, e := s.rooms.ListParticipants(ctx, &livekit.ListParticipantsRequest{Room: p.MediaRoom})
	if e == nil {
		// Revoke every previous screen grant before granting the new lease. This order
		// matters when an expired lease is reclaimed before the periodic reconciler.
		for _, remote := range members.Participants {
			if remote.Identity == p.Identity {
				continue
			}
			member, ok := s.store.Get(remote.Identity)
			if !ok {
				_, e = s.rooms.RemoveParticipant(ctx, &livekit.RoomParticipantIdentity{Room: p.MediaRoom, Identity: remote.Identity})
			} else {
				member.CanScreen = false
				_, e = s.rooms.UpdateParticipant(ctx, &livekit.UpdateParticipantRequest{Room: p.MediaRoom, Identity: remote.Identity, Permission: sessionPermission(member)})
			}
			if e != nil {
				break
			}
		}
	}
	if e == nil {
		current, ok := s.store.Get(p.Identity)
		if !ok {
			e = ErrAccess
		} else {
			_, e = s.rooms.UpdateParticipant(ctx, &livekit.UpdateParticipantRequest{Room: p.MediaRoom, Identity: p.Identity, Permission: sessionPermission(current)})
		}
	}
	if e != nil {
		if in.Claim {
			_ = s.store.Screen(p.Identity, false)
		}
		s.wakeReconcile()
		fail(w, 503, "Media permission confirmation pending; try again")
		return
	}
	reply(w, 200, map[string]bool{"canScreen": in.Claim})
}
