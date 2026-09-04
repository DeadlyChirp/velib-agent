package httpapi

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Limite de débit par client, sur les routes qui appellent le modèle.
//
// POURQUOI. Un tour d'agent consomme environ six mille jetons chez le
// fournisseur. Sans plafond, une boucle de dix lignes vide le quota en une
// minute, et le service devient indisponible pour tout le monde — mesuré pour
// de vrai pendant les bancs de ce projet, où un enchaînement de mesures a
// épuisé le palier gratuit et fait répondre 429 au reste des tests.
//
// Ce n'est pas une protection contre une attaque distribuée : celle-ci se
// traite en amont, chez l'hébergeur ou le répartiteur de charge. C'est le
// garde-fou qui empêche UN client, malveillant ou simplement buggé, de faire
// tomber le service pour les autres.
//
// Seul POST /messages est limité. Lire la liste des conversations ou la route
// de santé ne coûte rien : les brider gênerait une supervision légitime sans
// rien protéger.

// Réglages. Volontairement généreux pour un usage humain — poser six questions
// d'affilée reste possible — et serrés pour une boucle.
const (
	debitParMinute = 12 // jetons regagnés par minute
	debitRafale    = 6  // jetons disponibles d'emblée
	debitOubli     = 15 * time.Minute
)

// seau est un seau à jetons. On recalcule le niveau à la lecture plutôt que de
// faire tourner une horloge : pas de goroutine par client, et un client qui
// disparaît ne coûte rien jusqu'au nettoyage.
type seau struct {
	jetons float64
	vu     time.Time
}

type limiteur struct {
	mu         sync.Mutex
	seaux      map[string]*seau
	purge      time.Time
	maintenant func() time.Time // injectable pour les tests
}

func nouveauLimiteur() *limiteur {
	return &limiteur{
		seaux:      make(map[string]*seau),
		maintenant: time.Now,
	}
}

// autorise consomme un jeton pour cle et dit si la requête peut passer.
// Rend aussi le délai à attendre, pour l'en-tête Retry-After.
func (l *limiteur) autorise(cle string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.maintenant()
	l.nettoyer(now)

	s, connu := l.seaux[cle]
	if !connu {
		s = &seau{jetons: debitRafale, vu: now}
		l.seaux[cle] = s
	} else {
		regagne := now.Sub(s.vu).Minutes() * debitParMinute
		s.jetons = min(float64(debitRafale), s.jetons+regagne)
		s.vu = now
	}

	if s.jetons < 1 {
		manque := (1 - s.jetons) / debitParMinute // en minutes
		return false, time.Duration(manque * float64(time.Minute))
	}
	s.jetons--
	return true, 0
}

// nettoyer retire les clients oubliés depuis longtemps. Sans cela, la table
// grandit indéfiniment : une fuite mémoire lente, invisible en développement et
// visible au bout de quelques semaines en production.
//
// Balayage au plus une fois par minute, sous le verrou déjà tenu par autorise.
func (l *limiteur) nettoyer(now time.Time) {
	if now.Sub(l.purge) < time.Minute {
		return
	}
	l.purge = now
	for cle, s := range l.seaux {
		if now.Sub(s.vu) > debitOubli {
			delete(l.seaux, cle)
		}
	}
}

// clientDe identifie le demandeur.
//
// L'identité applicative d'abord, l'adresse IP ensuite. Aucune des deux n'est
// infalsifiable — c'est un garde-fou d'usage, pas un contrôle d'accès — mais
// la combinaison suffit à arrêter une boucle accidentelle et à ralentir
// nettement une boucle volontaire.
//
// X-Forwarded-For n'est PAS lu : l'en-tête est fourni par le client et se
// falsifie d'une ligne, ce qui donnerait une clé différente à chaque requête et
// contournerait la limite. Derrière un vrai répartiteur de charge, c'est à lui
// de porter cette responsabilité.
func clientDe(r *http.Request) string {
	if u := userID(r); u != "" && u != defaultUserID {
		return "u:" + u
	}
	hote, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		hote = r.RemoteAddr
	}
	return "ip:" + hote
}

// withRateLimit protège une route.
func (s *Server) withRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, attendre := s.debit.autorise(clientDe(r))
		if !ok {
			secondes := int(attendre.Seconds()) + 1
			w.Header().Set("Retry-After", strconv.Itoa(secondes))
			s.log.Warn("limite de débit atteinte", "client", clientDe(r),
				"chemin", r.URL.Path)
			writeError(w, http.StatusTooManyRequests,
				"trop de questions en peu de temps",
				"réessayez dans "+strconv.Itoa(secondes)+" secondes")
			return
		}
		next.ServeHTTP(w, r)
	})
}
