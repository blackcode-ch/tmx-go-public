package api

import (
	"net/http"

	"github.com/blackcode-ch/tmx-go/internal/fleet"
	"github.com/blackcode-ch/tmx-go/internal/people"
)

// People, Clients, Promises and Reminders (docs/PEOPLE_PROMISES.md). The fleet layer decides
// who may see or change what: the owner sees everything, agents the public view.
func (s *Server) peopleRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/team", s.listPeople)
	mux.HandleFunc("POST /api/team", s.putPerson)
	mux.HandleFunc("POST /api/team/import", s.ownerOnly(s.importPeople)) // before {key}
	mux.HandleFunc("GET /api/team/{key}", s.getPerson)
	mux.HandleFunc("DELETE /api/team/{key}", s.ownerOnly(s.deletePerson))
	mux.HandleFunc("GET /api/promises", s.listPromises)
	mux.HandleFunc("POST /api/promises", s.putPromise)
	mux.HandleFunc("POST /api/promises/{id}", s.putPromise)
	mux.HandleFunc("GET /api/reminders", s.listReminders)
	mux.HandleFunc("POST /api/reminders", s.addReminder)
	mux.HandleFunc("DELETE /api/reminders/{id}", s.cancelReminder)
}

func (s *Server) listPeople(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	out, err := s.f.People(c, r.URL.Query().Get("kind"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) getPerson(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	out, err := s.f.Person(c, r.PathValue("key"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) putPerson(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	var in fleet.PersonInput
	if !decode(w, r, &in) {
		return
	}
	p, err := s.f.PutPerson(c, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, p)
}

func (s *Server) deletePerson(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	if err := s.f.DeletePerson(c, r.PathValue("key")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) listPromises(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	q := r.URL.Query()
	out, err := s.f.Promises(c, q.Get("person"), q.Get("status"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) putPromise(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	var in fleet.PromiseInput
	if !decode(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	p, err := s.f.PutPromise(c, id, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	status := 200
	if id == "" {
		status = 201
	}
	writeJSON(w, status, p)
}

func (s *Server) listReminders(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	out, err := s.f.Reminders(c, r.URL.Query().Get("state"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) addReminder(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	var in fleet.ReminderInput
	if !decode(w, r, &in) {
		return
	}
	rm, err := s.f.AddReminder(c, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 201, rm)
}

func (s *Server) cancelReminder(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	if err := s.f.CancelReminder(c, r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) importPeople(w http.ResponseWriter, r *http.Request) {
	c, _ := caller(r)
	var plan people.Plan
	if !decodeMax(w, r, &plan, 8<<20) {
		return
	}
	rep, err := s.f.ImportPeople(c, plan)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, rep)
}
