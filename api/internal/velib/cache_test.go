package velib

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// fetcherLent simule la source reelle : elle met du temps, et on compte les
// appels pour verifier qu'on ne la sollicite pas plus que necessaire.
type fetcherLent struct {
	delai  time.Duration
	appels atomic.Int32
	err    error
}

func (f *fetcherLent) Fetch(ctx context.Context) (Snapshot, error) {
	f.appels.Add(1)
	select {
	case <-time.After(f.delai):
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	}
	if f.err != nil {
		return Snapshot{}, f.err
	}
	return Snapshot{
		Stations:  []Station{{ID: 1, Name: "Test", IsInstalled: true, IsRenting: true}},
		FetchedAt: time.Now(),
	}, nil
}

func cacheDeTest(f fetcher, ttl time.Duration) *Cache {
	return NewCache(f, WithTTL(ttl),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
}

// Le test qui justifie le rafraichissement en arriere-plan : une fois la donnee
// perimee, l'appelant ne doit PLUS payer le temps de la source.
func TestSnapshotPerimeSertSansAttendre(t *testing.T) {
	f := &fetcherLent{delai: 300 * time.Millisecond}
	c := cacheDeTest(f, 20*time.Millisecond)

	// Premier appel : rien en memoire, on accepte d'attendre.
	debut := time.Now()
	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatalf("premier appel : %v", err)
	}
	if d := time.Since(debut); d < 250*time.Millisecond {
		t.Fatalf("le demarrage a froid devrait attendre la source, a pris %v", d)
	}

	time.Sleep(40 * time.Millisecond) // laisse le TTL expirer

	// Deuxieme appel : perime. Doit repondre tout de suite.
	debut = time.Now()
	snap, err := c.Snapshot(context.Background())
	ecoule := time.Since(debut)
	if err != nil {
		t.Fatalf("appel sur donnee perimee : %v", err)
	}
	if len(snap.Stations) == 0 {
		t.Fatal("donnee vide alors que le cache en avait")
	}
	if ecoule > 50*time.Millisecond {
		t.Errorf("l'appelant a attendu %v : le rafraichissement bloque encore", ecoule)
	}
}

// Cent appels simultanes sur une donnee perimee ne doivent declencher qu'UN
// seul telechargement — c'est ce que garantit inFlight.
func TestUnSeulRafraichissementConcurrent(t *testing.T) {
	f := &fetcherLent{delai: 100 * time.Millisecond}
	c := cacheDeTest(f, 10*time.Millisecond)

	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatalf("amorcage : %v", err)
	}
	apresAmorcage := f.appels.Load()

	time.Sleep(20 * time.Millisecond)

	fini := make(chan struct{})
	for i := 0; i < 100; i++ {
		go func() { _, _ = c.Snapshot(context.Background()); fini <- struct{}{} }()
	}
	for i := 0; i < 100; i++ {
		<-fini
	}
	time.Sleep(150 * time.Millisecond) // laisse le rafraichissement finir

	if n := f.appels.Load() - apresAmorcage; n > 1 {
		t.Errorf("%d telechargements pour 100 appels simultanes, attendu 1", n)
	}
}

// Source en panne au demarrage : aucune donnee a servir, l'erreur doit remonter
// plutot que d'etre masquee par un snapshot vide.
func TestSourceEnPanneAuDemarrageRemonteLErreur(t *testing.T) {
	f := &fetcherLent{delai: time.Millisecond, err: errors.New("source injoignable")}
	c := cacheDeTest(f, time.Minute)

	if _, err := c.Snapshot(context.Background()); err == nil {
		t.Fatal("erreur attendue quand la source tombe et que le cache est vide")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Le chemin qui servait du périmé sans le dire
// ─────────────────────────────────────────────────────────────────────────────

// fetcherQuiTombe réussit le premier appel puis échoue à tous les suivants.
// Pas de champ écrit depuis le test pendant que la goroutine de fond lit : le
// basculement est porté par le compteur atomique lui-même.
type fetcherQuiTombe struct{ appels atomic.Int32 }

func (f *fetcherQuiTombe) Fetch(context.Context) (Snapshot, error) {
	if f.appels.Add(1) == 1 {
		return Snapshot{
			Stations:  []Station{{ID: 1, Name: "Test", IsInstalled: true, IsRenting: true}},
			FetchedAt: time.Now(),
		}, nil
	}
	return Snapshot{}, errors.New("source injoignable")
}

// Le chemin le plus fréquent du cache — servir tout de suite, rafraîchir
// derrière — ne savait pas dire qu'il servait du périmé.
//
// Pendant une panne de la source, il rendait des chiffres datés avec
// Stale=false et comptait des succès de cache. Le compteur de donnée périmée,
// qui existe précisément pour rendre l'incident visible, restait à zéro pendant
// l'incident. Le champ Stale se définit pourtant comme « servi alors que le
// rafraîchissement a échoué » : c'était exactement ce cas.
//
// Aucun test ne portait sur Stale avant celui-ci.
func TestPerimeMarqueQuandLeRafraichissementDeFondEchoue(t *testing.T) {
	f := &fetcherQuiTombe{}
	c := cacheDeTest(f, 20*time.Millisecond)

	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatalf("amorçage : %v", err)
	}

	// Passé le TTL, l'appel suivant prend le chemin non bloquant et déclenche
	// un rafraîchissement de fond, qui échouera.
	time.Sleep(40 * time.Millisecond)
	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("appel non bloquant : %v", err)
	}
	// Celui-ci est légitimement non marqué : à cet instant, aucun échec n'est
	// encore connu. C'est le suivant qui doit changer d'avis.
	if snap.Stale {
		t.Error("marqué périmé avant même qu'un rafraîchissement ait échoué")
	}

	// On laisse l'échec se produire et se publier.
	fin := time.Now().Add(2 * time.Second)
	for f.appels.Load() < 2 && time.Now().Before(fin) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)

	snap, err = c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("appel après échec : %v", err)
	}
	if !snap.Stale {
		t.Fatal("la source est tombée, le cache sert une donnée datée et la " +
			"présente comme fraîche : le modèle n'a aucun moyen de le dire")
	}
	if snap.StaleReason == "" {
		t.Error("Stale sans raison : l'agent ne peut rien expliquer à l'utilisateur")
	}
}

// Le miroir : tant que la source répond, ce chemin ne doit RIEN marquer. Un
// drapeau qui se lève à chaque expiration de TTL se lèverait en permanence, et
// un signal permanent cesse d'être lu.
func TestPerimeNeMarquePasQuandLaSourceRepond(t *testing.T) {
	f := &fetcherLent{delai: 5 * time.Millisecond}
	c := cacheDeTest(f, 20*time.Millisecond)

	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatalf("amorçage : %v", err)
	}
	for i := 0; i < 3; i++ {
		time.Sleep(30 * time.Millisecond)
		snap, err := c.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("tour %d : %v", i, err)
		}
		if snap.Stale {
			t.Fatalf("tour %d : marqué périmé alors que la source répond, "+
				"raison = %q", i, snap.StaleReason)
		}
	}
}
