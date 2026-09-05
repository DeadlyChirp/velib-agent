package httpapi

import (
	"net/http"
	"strconv"
	"time"
)

// Plafond de requêtes simultanées touchant la base.
//
// ── POURQUOI, ET COMMENT ÇA A ÉTÉ TROUVÉ ────────────────────────────────────
//
// Mesuré par audit/charge.py. Jusqu'à 100 clients simultanés, le service tient
// sans une erreur : 668 requêtes par seconde, p95 à 257 ms. À 200 clients, il
// renvoie des centaines de 500 — et PostgreSQL dit pourquoi :
//
//	FATAL: sorry, too many clients already
//
// La cause est en amont de ce fichier. Le service de sessions du framework
// ouvre sa base avec sql.Open sans jamais appeler SetMaxOpenConns, et
// database/sql autorise alors un nombre ILLIMITÉ de connexions. Chaque requête
// concurrente en réclame une, PostgreSQL plafonne à 100 par défaut, et
// au-delà il refuse.
//
// Le framework n'expose ni le *sql.DB ni d'option de pool : on ne peut pas
// corriger la cause depuis ici. On peut en revanche empêcher d'y arriver.
//
// ── POURQUOI PAS SIMPLEMENT AUGMENTER max_connections ───────────────────────
//
// Parce que ça déplace le mur sans le supprimer. Chaque connexion PostgreSQL
// coûte de la mémoire, et un service qui en ouvre autant qu'il reçoit de
// requêtes finit toujours par en manquer — plus tard, sous une charge plus
// grosse, et cette fois en production.
//
// ── CE QUE FAIT CE PLAFOND ──────────────────────────────────────────────────
//
// Il transforme une panne en attente, puis en refus explicite. Sous le plafond,
// rien ne change. Au-dessus, la requête patiente brièvement — la plupart des
// pointes se lissent en quelques dizaines de millisecondes. Si le service est
// vraiment saturé, elle repart avec un 503 et un Retry-After.
//
// 503 et non 500 : le premier dit « revenez », le second dit « nous sommes
// cassés ». Un client automatique sait retenter sur un 503, et une supervision
// ne réveille personne pour une pointe de charge.

const (
	// Confortablement sous les 100 connexions par défaut de PostgreSQL, en
	// laissant de la marge pour la sonde de santé, les migrations et une
	// session psql ouverte pour diagnostiquer.
	maxEnVol = 64

	// Attente maximale avant de refuser. Assez pour absorber une pointe, assez
	// court pour ne pas laisser un client suspendu : au-delà, il vaut mieux une
	// réponse franche qu'une connexion qui traîne.
	attenteMax = 2 * time.Second
)

// vigie borne le nombre de requêtes en vol. Un canal tamponné suffit : chaque
// jeton pris est une place occupée, et le rendre en `defer` garantit qu'aucune
// place ne fuit, même si le gestionnaire panique.
type vigie struct {
	places chan struct{}
}

func nouvelleVigie(n int) *vigie {
	return &vigie{places: make(chan struct{}, n)}
}

// prendre tente d'occuper une place, en patientant au plus attenteMax.
func (v *vigie) prendre(quitter <-chan struct{}) bool {
	select {
	case v.places <- struct{}{}:
		return true
	default:
	}

	// Plus de place libre : on patiente, sans jamais dépasser le délai.
	minuteur := time.NewTimer(attenteMax)
	defer minuteur.Stop()

	select {
	case v.places <- struct{}{}:
		return true
	case <-quitter:
		// Le client est parti. Inutile de lui garder une place.
		return false
	case <-minuteur.C:
		return false
	}
}

func (v *vigie) rendre() { <-v.places }

// EnVol rend le nombre de requêtes actuellement en vol, pour les métriques.
func (v *vigie) EnVol() int { return len(v.places) }

// withMaxEnVol protège les routes qui touchent la base.
//
// La route de santé et les métriques en sont exclues à dessein : elles servent
// justement à diagnostiquer une saturation, et les brider reviendrait à
// aveugler la supervision au moment précis où elle est utile.
func (s *Server) withMaxEnVol(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.vigie.prendre(r.Context().Done()) {
			// Si le client est déjà parti, inutile d'écrire quoi que ce soit.
			if r.Context().Err() != nil {
				return
			}
			w.Header().Set("Retry-After", strconv.Itoa(1))
			s.log.Warn("service saturé, requête refusée",
				"chemin", r.URL.Path, "en_vol", s.vigie.EnVol())
			writeError(w, http.StatusServiceUnavailable,
				"service momentanément saturé",
				"réessayez dans un instant")
			return
		}
		defer s.vigie.rendre()
		next.ServeHTTP(w, r)
	})
}
