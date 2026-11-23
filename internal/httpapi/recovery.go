package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Jonasz1996/clusterforge/internal/auth"
	"github.com/Jonasz1996/clusterforge/internal/events"
	"github.com/Jonasz1996/clusterforge/internal/httpapi/gen"
)

// AccountRecoveryHelp geeft onder "Kan niet inloggen?" het commando waarmee
// een beheerder het account herstelt. Het antwoord hangt alleen af van de
// ingevulde naam: de database wordt niet geraadpleegd, dus tekst noch
// responstijd verraden of het account bestaat.
func (s *Server) AccountRecoveryHelp(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ip := clientIP(r)
	if !s.recoveryLimiter.allow(ip) {
		s.throttle.Write(ctx, ip, events.Event{
			Actor: events.System(), SubjectType: "auth", SubjectID: "recovery", Action: "auth.rate_limited",
			Payload: map[string]any{"ip": ip, "what": "recovery"},
		})
		w.Header().Set("Retry-After", "20")
		writeError(w, http.StatusTooManyRequests, "rate_limited", "te veel aanvragen, probeer het straks opnieuw")
		return
	}
	var req gen.RecoveryRequest
	if !decode(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Username)
	// Alleen een naam die als gebruikersnaam kan bestaan, komt in een
	// commando dat iemand in een shell plakt.
	if !auth.ValidUsername(name) {
		writeError(w, http.StatusBadRequest, "validation", "vul je gebruikersnaam in: letters, cijfers, punt, streepje of underscore")
		return
	}
	// Wat er ingevuld werd, komt niet in het logboek: dat zou een lijst
	// met geprobeerde namen worden.
	s.throttle.Write(ctx, ip, events.Event{
		Actor: events.System(), SubjectType: "auth", SubjectID: "recovery", Action: "auth.recovery_requested",
		Payload: map[string]any{"ip": ip},
	})
	writeJSON(w, http.StatusOK, gen.RecoveryHelp{
		Commands:   recoveryCommands(s.cfg.ResetCommand, name),
		TtlMinutes: int(auth.TempPasswordTTL.Minutes()),
	})
}

func recoveryCommands(custom, name string) []gen.RecoveryCommand {
	type cmd = gen.RecoveryCommand
	if custom != "" {
		return []cmd{{Label: "Op de server", Command: strings.ReplaceAll(custom, "{user}", name)}}
	}
	return []cmd{
		{Label: "Docker Compose: op de server, in de map van docker-compose.yml (bv. /opt/clusterforge/deploy)", Command: "docker compose exec server clusterforge-server user reset " + name},
		{Label: "ClusterForge-cluster (HA): op een van de nodes", Command: "docker exec cf-clusterforge-server clusterforge-server user reset " + name},
	}
}

// CompleteResetPassword zet na een reset het nieuwe wachtwoord.
func (s *Server) CompleteResetPassword(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	var req gen.ResetPasswordRequest
	if !decode(w, r, &req) {
		return
	}
	err := s.auth.CompleteForcedPassword(r.Context(), p, req.NewPassword)
	if errors.Is(err, auth.ErrNoForcedChange) {
		writeError(w, http.StatusConflict, "no_forced_change", err.Error())
		return
	}
	if s.authError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
