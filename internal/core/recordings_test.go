package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ilguzin/checkpoint/internal/minutes"
)

func recordingSetup(t *testing.T) (*Server, User, Room, Session, minutes.Config) {
	t.Helper()
	s := setup(t)
	u := admin(t, s.store)
	r := mustRoom(t, s.store, u, "meeting")
	p, _, e := s.store.JoinRoom(u.ID, r.ID)
	if e != nil {
		t.Fatal(e)
	}
	c := minutes.DefaultConfig()
	c.Enabled = true
	c.Storage = t.TempDir()
	c.Transcription.Adapter = "api"
	c.Transcription.Endpoint = "https://example.invalid/transcribe"
	c.Transcription.Revision = "test"
	c.Generator.Revision = "test"
	b, _ := json.Marshal(c)
	s.cfg.MinutesConfig = filepath.Join(t.TempDir(), "minutes.yaml")
	if e = os.WriteFile(s.cfg.MinutesConfig, b, 0600); e != nil {
		t.Fatal(e)
	}
	_, e = s.store.db.Exec("INSERT INTO media_workers VALUES('recorder',unixepoch(),1),('processor',unixepoch(),1)")
	if e != nil {
		t.Fatal(e)
	}
	return s, u, r, p, c
}
func startTestRecording(t *testing.T, s *Server, p Session, c minutes.Config, wants bool) minutes.Run {
	t.Helper()
	r, e := s.store.startRecording(p.Identity, randomID(), wants, c)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func saveTestRecording(t *testing.T, s *Server, r minutes.Run, c minutes.Config) minutes.Manifest {
	t.Helper()
	dir := filepath.Join(c.Storage, r.ID)
	if e := os.MkdirAll(filepath.Join(dir, "sources"), 0700); e != nil {
		t.Fatal(e)
	}
	body := []byte("test-only verified media fixture")
	if e := os.WriteFile(filepath.Join(dir, "mix.m4a"), body, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "sources", "source-0000.webm"), []byte("individual"), 0600); e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(body)
	m := minutes.Manifest{Run: r.ID, Started: time.Now().Unix() - 20, Ended: time.Now().Unix(), MixHash: hex.EncodeToString(sum[:]), MixBytes: int64(len(body)), Sources: []minutes.Source{{ID: "source-0000", File: "sources/source-0000.webm", Packets: 1, Name: "Alice"}}}
	b, _ := json.Marshal(m)
	_, e := s.store.db.Exec("UPDATE recording_runs SET state='saved',started=?,ended=?,manifest=?,mix_bytes=? WHERE id=?", m.Started, m.Ended, string(b), m.MixBytes, r.ID)
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func TestRecordingIdempotencyDeadlineAndAuthorization(t *testing.T) {
	s, _, room, p, c := recordingSetup(t)
	key := randomID()
	r, e := s.store.startRecording(p.Identity, key, false, c)
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.store.startRecording(p.Identity, key, false, c)
	if e != nil || again.ID != r.ID {
		t.Fatal("start is not idempotent", e)
	}
	if _, e = s.store.startRecording(p.Identity, randomID(), false, c); e == nil {
		t.Fatal("second simultaneous recording accepted")
	}
	guest, _, _ := s.store.JoinRoomID(room.ID, "guest")
	if e = s.store.RecordingControl(room.ID, r.ID, guest.Identity, "stop", 0, false); e != ErrAccess {
		t.Fatal("guest controlled recording", e)
	}
	task, e := s.claimMedia("recorder", true)
	if e != nil || task == nil {
		t.Fatal(e)
	}
	d, e := s.mediaHeartbeat("recorder", minutes.Heartbeat{Run: r.ID, Lease: task.Lease, Phase: "recording", Started: time.Now().Unix()})
	if e != nil || d.Deadline != time.Now().Unix()+7200 {
		t.Fatal(d, e)
	}
	if e = s.store.RecordingControl(room.ID, r.ID, p.Identity, "extend", 0, false); e == nil {
		t.Fatal("early extension accepted")
	}
	_, _ = s.store.db.Exec("UPDATE recording_runs SET deadline=unixepoch()+120 WHERE id=?", r.ID)
	if e = s.store.RecordingControl(room.ID, r.ID, p.Identity, "extend", 0, false); e != nil {
		t.Fatal(e)
	}
	if e = s.store.RecordingControl(room.ID, r.ID, p.Identity, "extend", 0, false); e == nil {
		t.Fatal("duplicate extension accepted")
	}
	_, _ = s.store.db.Exec("UPDATE recording_runs SET deadline=unixepoch()-1 WHERE id=?", r.ID)
	s.maintainMediaOnce()
	r, _, e = scanRun(s.store.db.QueryRow(runSelect+" WHERE id=?", r.ID))
	if e != nil || r.State != "finalizing" || r.StopReason != "confirmation_timeout" {
		t.Fatal(r, e)
	}
}
func TestMinutesCancelFencesLateResultAndKeepsMix(t *testing.T) {
	s, _, room, p, c := recordingSetup(t)
	r := startTestRecording(t, s, p, c, true)
	saveTestRecording(t, s, r, c)
	if e := s.store.RecordingControl(room.ID, r.ID, p.Identity, "generate", 0, false); e != nil {
		t.Fatal(e)
	}
	task, e := s.claimMedia("processor", true)
	if e != nil || task == nil {
		t.Fatal(e)
	}
	if e = s.store.RecordingControl(room.ID, r.ID, p.Identity, "cancel", 0, false); e == nil {
		t.Fatal("cancel without confirmation")
	}
	if e = s.store.RecordingControl(room.ID, r.ID, p.Identity, "cancel", 0, true); e != nil {
		t.Fatal(e)
	}
	if e = s.mediaResult("processor", minutes.Result{Run: r.ID, Lease: task.Lease, Phase: "ready", Markdown: "must not publish"}); e != nil {
		t.Fatal(e)
	}
	detail, e := s.store.recordingDetail(room.ID, r.ID)
	if e != nil || detail.Processing != "cancelled" || len(detail.Revisions) != 0 {
		t.Fatal(detail, e)
	}
	if _, e = os.Stat(filepath.Join(c.Storage, r.ID, "mix.m4a")); e != nil {
		t.Fatal("cancel removed mix")
	}
}
func TestTranscriptCleanupAndGeneratorRetry(t *testing.T) {
	s, _, room, p, c := recordingSetup(t)
	r := startTestRecording(t, s, p, c, true)
	saveTestRecording(t, s, r, c)
	if e := s.store.RecordingControl(room.ID, r.ID, p.Identity, "generate", 0, false); e != nil {
		t.Fatal(e)
	}
	task, e := s.claimMedia("processor", true)
	if e != nil || task == nil {
		t.Fatal(e)
	}
	segments := []minutes.Segment{{ID: "S000001", Source: "source-0000", Speaker: "Alice", StartMS: 0, EndMS: 1000, Text: "Ship on Friday", Language: "en"}}
	if e = s.mediaResult("processor", minutes.Result{Run: r.ID, Lease: task.Lease, Phase: "transcript", Transcript: segments}); e != nil {
		t.Fatal(e)
	}
	s.cleanupMedia(c)
	if _, e = os.Stat(filepath.Join(c.Storage, r.ID, "sources")); !os.IsNotExist(e) {
		t.Fatal("individual audio retained after validated ASR", e)
	}
	if e = s.mediaResult("processor", minutes.Result{Run: r.ID, Lease: task.Lease, Error: "provider failed"}); e != nil {
		t.Fatal(e)
	}
	_, _ = s.store.db.Exec("UPDATE processing_jobs SET available_at=0 WHERE run_id=?", r.ID)
	next, e := s.claimMedia("processor", true)
	if e != nil || next == nil || len(next.Transcript) != 1 {
		t.Fatal("retry lost transcript", e)
	}
	if e = s.mediaResult("processor", minutes.Result{Run: r.ID, Lease: task.Lease, Phase: "ready", Markdown: "stale"}); e == nil {
		t.Fatal("stale lease published")
	}
	if e = s.mediaResult("processor", minutes.Result{Run: r.ID, Lease: next.Lease, Phase: "ready", Markdown: "# Minutes"}); e != nil {
		t.Fatal(e)
	}
	if e = s.store.RecordingControl(room.ID, r.ID, p.Identity, "cancel", 0, true); e == nil {
		t.Fatal("completed minutes cancelled")
	}
}
func TestMembershipAdmissionLegacyExpiryAndDelete(t *testing.T) {
	s, owner, room, host, c := recordingSetup(t)
	visitor := localTestUser(t, s.store, "visitor")
	_, _ = s.store.db.Exec("UPDATE rooms SET waiting_room=1 WHERE id=?", room.ID)
	p, _, _ := s.store.JoinRoomID(room.ID, "visitor")
	p, e := s.store.attachAccount(p, visitor.ID)
	if e != nil {
		t.Fatal(e)
	}
	rooms, e := s.store.Rooms(visitor.ID)
	if e != nil || len(rooms) != 0 {
		t.Fatal("waiting user got membership")
	}
	if e = s.store.Moderate(host.Identity, p.Identity, "admit", false); e != nil {
		t.Fatal(e)
	}
	rooms, e = s.store.Rooms(visitor.ID)
	if e != nil || len(rooms) != 1 || rooms[0].AccessRole != "shared" {
		t.Fatal(rooms, e)
	}
	if _, ok := s.store.Get(p.Identity); !ok {
		t.Fatal("ordinary account session rejected")
	}
	r := startTestRecording(t, s, host, c, false)
	saveTestRecording(t, s, r, c)
	if e = s.store.DeleteRoom(visitor.ID, room.ID, true, c.Storage); e != ErrAccess {
		t.Fatal("shared user deleted room")
	}
	if e = s.store.DeleteRoom(owner.ID, room.ID, false, c.Storage); e == nil {
		t.Fatal("delete without confirmation")
	}
	if e = s.store.DeleteRoom(owner.ID, room.ID, true, c.Storage); e != nil {
		t.Fatal(e)
	}
	if _, ok := s.store.Get(host.Identity); ok {
		t.Fatal("delete did not revoke session")
	}
	s.cleanupMedia(c)
	if _, e = os.Stat(filepath.Join(c.Storage, r.ID)); !os.IsNotExist(e) {
		t.Fatal("room artifacts survived deletion")
	}
	var name, state string
	if e = s.store.db.QueryRow("SELECT name,deletion_state FROM rooms WHERE id=?", room.ID).Scan(&name, &state); e != nil || name != "" || state != "deleted" {
		t.Fatal(name, state, e)
	}
}
func TestHistoryHTTPAdmissionAndCrossRoom(t *testing.T) {
	s, _, room, p, c := recordingSetup(t)
	r := startTestRecording(t, s, p, c, false)
	saveTestRecording(t, s, r, c)
	_, secret, _ := s.store.JoinRoomID(room.ID, "guest")
	handler := s.Handler(http.NotFoundHandler())
	call := func(path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	if w := call("/api/rooms/"+room.ID+"/history", secret); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("/api/rooms/other/history", secret); w.Code != 403 {
		t.Fatal("cross room", w.Code)
	}
	if w := call("/api/rooms/"+room.ID+"/recordings/"+r.ID+"/audio", secret); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	_, _ = s.store.db.Exec("UPDATE sessions SET admitted=0 WHERE secret_hash=?", hash(secret))
	if w := call("/api/rooms/"+room.ID+"/history", secret); w.Code != 403 {
		t.Fatal("waiting archive access")
	}
	if w := call("/api/rooms/"+room.ID+"/recordings/..%2f..%2fsecret/audio", strings.Repeat("x", 64)); w.Code == 200 {
		t.Fatal("unsafe path")
	}
}

func TestRecorderCrashDoesNotResumeAndOldLeaseCannotUpload(t *testing.T) {
	s, _, _, p, c := recordingSetup(t)
	r := startTestRecording(t, s, p, c, false)
	first, e := s.claimMedia("recorder", true)
	if e != nil || first == nil {
		t.Fatal(e)
	}
	_, _ = s.store.db.Exec("UPDATE recording_runs SET recorder_until=unixepoch()-1 WHERE id=?", r.ID)
	recovery, e := s.claimMedia("recorder", true)
	if e != nil || recovery == nil || recovery.Run.State != "finalizing" {
		t.Fatal("interrupted start resumed", recovery, e)
	}
	if _, e = s.artifactLease("recorder", r.ID, first.Lease); e != ErrAccess {
		t.Fatal("old recorder can upload", e)
	}
	if _, e = s.artifactLease("processor", r.ID, recovery.Lease); e != ErrAccess {
		t.Fatal("recorder lease grants processor access", e)
	}
}
func TestEmptyRoomIgnoresRecorderAndReturnsResetTimer(t *testing.T) {
	s, _, _, p, c := recordingSetup(t)
	c.EmptySeconds = 5
	r := startTestRecording(t, s, p, c, false)
	_, _ = s.store.db.Exec("UPDATE recording_runs SET state='recording',started=unixepoch(),deadline=unixepoch()+7200,empty_since=unixepoch()-10 WHERE id=?", r.ID)
	s.recordingPresence(p.MediaRoom, map[string]bool{p.Identity: true})
	var empty int64
	_ = s.store.db.QueryRow("SELECT empty_since FROM recording_runs WHERE id=?", r.ID).Scan(&empty)
	if empty != 0 {
		t.Fatal("return did not reset empty timer")
	}
	_, _ = s.store.db.Exec("UPDATE recording_runs SET empty_since=unixepoch()-10 WHERE id=?", r.ID)
	s.recordingPresence(p.MediaRoom, map[string]bool{"rec-service": true})
	current, _, e := scanRun(s.store.db.QueryRow(runSelect+" WHERE id=?", r.ID))
	if e != nil || current.State != "finalizing" || current.StopReason != "empty_room" {
		t.Fatal(current, e)
	}
}
func TestDeletionLedgerReappliedBeforeRestoredAccess(t *testing.T) {
	s, owner, room, p, c := recordingSetup(t)
	if e := s.store.DeleteRoom(owner.ID, room.ID, true, c.Storage); e != nil {
		t.Fatal(e)
	}
	_, _ = s.store.db.Exec("UPDATE rooms SET closed=0,deletion_state='' WHERE id=?", room.ID)
	_, _ = s.store.db.Exec("UPDATE sessions SET revoked=0 WHERE identity=?", p.Identity)
	if e := s.PrepareMedia(); e != nil {
		t.Fatal(e)
	}
	if _, ok := s.store.Get(p.Identity); ok {
		t.Fatal("restore resurrected deleted room")
	}
}

func TestAdminCanRecordButCannotDelete(t *testing.T) {
	s, owner, room, _, c := recordingSetup(t)
	adminUser := localTestUser(t, s.store, "recording-admin")
	_, code, e := s.store.CreateRoomAdminInvite(owner.ID, room.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.store.AcceptRoomAdminInvite(adminUser.ID, code); e != nil {
		t.Fatal(e)
	}
	p, _, e := s.store.JoinRoom(adminUser.ID, room.ID)
	if e != nil {
		t.Fatal(e)
	}
	r := startTestRecording(t, s, p, c, false)
	if e = s.store.RecordingControl(room.ID, r.ID, p.Identity, "stop", 0, false); e != nil {
		t.Fatal(e)
	}
	if e = s.store.DeleteRoom(adminUser.ID, room.ID, true, c.Storage); e != ErrAccess {
		t.Fatal("admin deleted room", e)
	}
}

func TestRecordingDeleteLedgerAndOptionalBadConfig(t *testing.T) {
	s, u, room, host, c := recordingSetup(t)
	run := startTestRecording(t, s, host, c, false)
	saveTestRecording(t, s, run, c)
	if err := s.store.deleteRecording(room.ID, run.ID, u.ID, false); err == nil {
		t.Fatal("missing confirmation accepted")
	}
	if err := s.store.deleteRecording(room.ID, run.ID, u.ID, true); err != nil {
		t.Fatal(err)
	}
	// Simulate restoring an older DB while keeping the independent deletion ledger.
	if _, err := s.store.db.Exec("UPDATE recording_runs SET state='saved' WHERE id=?", run.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.cfg.MinutesConfig, []byte("invalid: ["), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareMedia(); err != nil {
		t.Fatal("optional config prevented ledger reconciliation", err)
	}
	s.cleanupMedia(c)
	var state string
	if err := s.store.db.QueryRow("SELECT state FROM recording_runs WHERE id=?", run.ID).Scan(&state); err != nil || state != "deleted" {
		t.Fatal(state, err)
	}
	var closed bool
	if err := s.store.db.QueryRow("SELECT closed FROM rooms WHERE id=?", room.ID).Scan(&closed); err != nil || closed {
		t.Fatal("record deletion removed room", err)
	}
}

func TestRecordingStatusDistinguishesMinutesReadiness(t *testing.T) {
	s, _, room, _, c := recordingSetup(t)
	_, secret, err := s.store.JoinRoomID(room.ID, "status guest")
	if err != nil {
		t.Fatal(err)
	}
	handler := s.Handler(http.NotFoundHandler())
	check := func(want bool) {
		t.Helper()
		w := request(handler, "GET", "/api/recording", secret, "")
		var status struct {
			Enabled      bool
			Ready        bool
			MinutesReady bool
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &status) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		if !status.Enabled || !status.Ready || status.MinutesReady != want {
			t.Fatalf("unexpected readiness: %+v", status)
		}
	}
	check(true)
	if _, err = s.store.db.Exec("UPDATE media_workers SET seen=unixepoch()-30 WHERE kind='processor'"); err != nil {
		t.Fatal(err)
	}
	check(false)
	if _, err = s.store.db.Exec("UPDATE media_workers SET seen=unixepoch() WHERE kind='processor'"); err != nil {
		t.Fatal(err)
	}
	c.Transcription.Adapter = "faster-whisper"
	c.Transcription.Python = filepath.Join(t.TempDir(), "missing-python")
	body, _ := json.Marshal(c)
	if err = os.WriteFile(s.cfg.MinutesConfig, body, 0600); err != nil {
		t.Fatal(err)
	}
	check(false)
}
