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
