package core

import "net/http"

func (s *Server) roomAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/rooms/{room}/admin-invitations", s.withAccount(func(w http.ResponseWriter, r *http.Request, u User) {
		invite, secret, err := s.store.CreateRoomAdminInvite(u.ID, r.PathValue("room"))
		if err != nil {
			accessError(w, err)
			return
		}
		reply(w, 201, map[string]any{"invitation": invite, "url": s.cfg.PublicOrigin + "/#adminInvite=" + secret})
	}))
	mux.HandleFunc("POST /api/room-admin-invitations/accept", s.withAccount(func(w http.ResponseWriter, r *http.Request, u User) {
		var in struct{ Invite string }
		if !decode(w, r, &in) {
			return
		}
		room, err := s.store.AcceptRoomAdminInvite(u.ID, in.Invite)
		if err != nil {
			accessError(w, err)
			return
		}
		reply(w, 200, room)
	}))
	mux.HandleFunc("GET /api/rooms/{room}/admins", s.withAccount(func(w http.ResponseWriter, r *http.Request, u User) {
		users, err := s.store.RoomAdmins(u.ID, r.PathValue("room"))
		if err != nil {
			accessError(w, err)
			return
		}
		reply(w, 200, users)
	}))
	mux.HandleFunc("POST /api/rooms/{room}/admins/{id}/remove", s.withAccount(func(w http.ResponseWriter, r *http.Request, u User) {
		s.permissions.Lock()
		defer s.permissions.Unlock()
		if err := s.store.RemoveRoomAdmin(u.ID, r.PathValue("room"), r.PathValue("id")); err != nil {
			accessError(w, err)
			return
		}
		s.wakeReconcile()
		reply(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/rooms/{room}/admin-invitations/{id}/revoke", s.withAccount(func(w http.ResponseWriter, r *http.Request, u User) {
		if err := s.store.RevokeRoomAdminInvite(u.ID, r.PathValue("room"), r.PathValue("id")); err != nil {
			accessError(w, err)
			return
		}
		reply(w, 200, map[string]bool{"ok": true})
	}))
}
