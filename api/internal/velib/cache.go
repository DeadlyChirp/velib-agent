package velib

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Recorder reçoit les événements du cache.
//
// L'interface est déclarée ICI, du côté qui consomme, plutôt que d'importer le
// paquet d'observabilité. Le métier ne dépend donc pas de la métrologie :
// internal/velib reste testable seul, et on peut brancher un autre collecteur
// sans toucher à cette ligne.
type Recorder interface {
	RecordCacheHit()
	RecordCacheRefresh(stations int, err error)
	RecordStaleServed()
}

// noopRecorder est le défaut : compter n'est pas obligatoire pour fonctionner.
type noopRecorder struct{}

func (noopRecorder) RecordCacheHit()               {}
func (noopRecorder) RecordCacheRefresh(int, error) {}
func (noopRecorder) RecordStaleServed()            {}

// Source est ce que les outils utilisent. L'interface existe pour que les tests
// puissent injecter un parc figé sans toucher au réseau.
type Source interface {
	Snapshot(ctx context.Context) (Snapshot, error)
}

// fetcher est la dépendance réelle du cache, isolée pour les tests.
type fetcher interface {
	Fetch(ctx context.Context) (Snapshot, error)
}

// Cache sert le parc avec une politique de fraîcheur explicite.
//
// Trois comportements, dans cet ordre :
//
//  1. Donnée fraîche en mémoire  ->  servie immédiatement, aucun appel réseau.
//  2. Donnée périmée             ->  UN SEUL rafraîchissement, même si cent
//     requêtes arrivent en même temps.
//  3. Rafraîchissement en échec  ->  on sert la dernière donnée connue en la
//     MARQUANT périmée, plutôt que de renvoyer une erreur.
//
// Le point 3 est délibéré. Un agent qui reçoit une erreur brute a tendance à
// combler le vide en inventant. Un agent qui reçoit « voici la donnée, elle a
// quarante minutes, la source est injoignable » peut le dire honnêtement à
// l'utilisateur. Une panne qui crie coûte moins cher qu'une panne qui se tait.
type Cache struct {
	fetcher fetcher
	ttl     time.Duration
	log     *slog.Logger

	rec Recorder

	mu       sync.Mutex
	current  Snapshot
	hasData  bool
	inFlight *call // rafraîchissement en cours, partagé entre appelants
}

// call porte un rafraîchissement en cours. C'est un singleflight minimal :
// plutôt que d'ajouter une dépendance pour une seule clé, on garde les vingt
// lignes qui font le travail et qu'on peut défendre en revue.
type call struct {
	done chan struct{}
	snap Snapshot
	err  error
}

// CacheOption configure le cache.
type CacheOption func(*Cache)

// WithTTL fixe la durée de validité du parc en mémoire.
func WithTTL(d time.Duration) CacheOption { return func(c *Cache) { c.ttl = d } }

// WithLogger branche un journal structuré.
func WithLogger(l *slog.Logger) CacheOption { return func(c *Cache) { c.log = l } }

// WithRecorder branche un collecteur de métriques.
func WithRecorder(r Recorder) CacheOption {
	return func(c *Cache) {
		if r != nil {
			c.rec = r
		}
	}
}

// NewCache construit le cache.
//
// Le TTL par défaut est de 60 secondes. Le fichier annonce lui-même un ttl de
// 3600 s, mais la donnée bouge à la minute : une minute est le compromis entre
// fraîcheur et respect d'un service public gratuit. Sans cache, chaque appel
// d'outil retéléchargerait 464 Ko, et une question déclenchant trois appels
// coûterait 1,4 Mo pour une donnée identique.
func NewCache(f fetcher, opts ...CacheOption) *Cache {
	c := &Cache{
		fetcher: f,
		ttl:     60 * time.Second,
		log:     slog.Default(),
		rec:     noopRecorder{},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Snapshot rend le parc, frais si possible, daté sinon.
func (c *Cache) Snapshot(ctx context.Context) (Snapshot, error) {
	c.mu.Lock()

	// 1. Donnée encore valide : rien à faire.
	if c.hasData && time.Since(c.current.FetchedAt) < c.ttl {
		snap := c.current
		c.mu.Unlock()
		c.rec.RecordCacheHit()
		return snap, nil
	}

	// 2. Un rafraîchissement est déjà en vol : on s'y raccroche au lieu d'en
	//    lancer un deuxième. Sans ça, dix questions simultanées après
	//    expiration déclenchent dix téléchargements de 464 Ko.
	if c.inFlight != nil {
		waitOn := c.inFlight
		stale, hasStale := c.current, c.hasData
		c.mu.Unlock()

		select {
		case <-ctx.Done():
			// L'appelant abandonne : on ne bloque pas, on sert ce qu'on a.
			if hasStale {
				return markStale(stale, "requête interrompue pendant le rafraîchissement"), nil
			}
			return Snapshot{}, ctx.Err()
		case <-waitOn.done:
			if waitOn.err == nil {
				return waitOn.snap, nil
			}
			if hasStale {
				return markStale(stale, waitOn.err.Error()), nil
			}
			return Snapshot{}, waitOn.err
		}
	}

	// 3. C'est à nous de rafraîchir.
	cl := &call{done: make(chan struct{})}
	c.inFlight = cl
	stale, hasStale := c.current, c.hasData
	c.mu.Unlock()

	// Le rafraîchissement a son propre plafond de temps, indépendant de celui
	// de l'appelant : si l'utilisateur ferme son navigateur, le parc doit
	// quand même finir de se charger pour les requêtes suivantes.
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	snap, err := c.fetcher.Fetch(fetchCtx)
	cancel()
	c.rec.RecordCacheRefresh(len(snap.Stations), err)

	c.mu.Lock()
	if err == nil {
		c.current = snap
		c.hasData = true
	}
	cl.snap, cl.err = snap, err
	c.inFlight = nil
	close(cl.done)
	c.mu.Unlock()

	if err != nil {
		if hasStale {
			// Le cas qui compte : la source est tombée mais le service tient.
			c.rec.RecordStaleServed()
			c.log.Warn("source Vélib injoignable, service de la dernière donnée connue",
				"erreur", err,
				"age_donnee_s", int64(time.Since(stale.FetchedAt).Seconds()))
			return markStale(stale, err.Error()), nil
		}
		// Premier démarrage et source déjà en panne : là, on ne peut rien
		// inventer, l'erreur remonte.
		c.log.Error("source Vélib injoignable et aucune donnée en cache", "erreur", err)
		return Snapshot{}, err
	}

	c.log.Info("parc Vélib rafraîchi", "stations", len(snap.Stations))
	return snap, nil
}

// markStale recopie un snapshot en le marquant périmé. On copie plutôt que de
// muter : le snapshot en cache est partagé entre goroutines.
func markStale(s Snapshot, reason string) Snapshot {
	s.Stale = true
	s.StaleReason = reason
	return s
}

// Warm charge le parc au démarrage.
//
// Sans préchauffage, le tout premier utilisateur paie deux téléchargements
// pendant que le modèle attend, et l'appel d'outil peut expirer. Un échec ici
// n'est PAS fatal : le service démarre quand même et réessaiera au premier
// appel. Refuser de démarrer parce qu'une source tierce est momentanément
// indisponible transformerait une panne partielle en panne totale.
func (c *Cache) Warm(ctx context.Context) {
	if _, err := c.Snapshot(ctx); err != nil {
		c.log.Warn("préchauffage du parc en échec, démarrage quand même", "erreur", err)
	}
}
