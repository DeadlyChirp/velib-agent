package httpapi

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// La vigie borne le nombre de requêtes qui touchent la base en même temps.
// Sans elle, mesuré à 200 clients simultanés : PostgreSQL répond
// « sorry, too many clients already » et le service renvoie des 500.

func TestVigieBorneLesPlacesSimultanees(t *testing.T) {
	const places = 3
	v := nouvelleVigie(places)

	for i := 0; i < places; i++ {
		if !v.prendre(nil) {
			t.Fatalf("place %d refusée alors que %d sont disponibles", i+1, places)
		}
	}
	if v.EnVol() != places {
		t.Errorf("en vol = %d, attendu %d", v.EnVol(), places)
	}

	// La place suivante doit attendre, puis échouer. On ne veut pas patienter
	// les deux secondes du plafond réel dans un test : on vérifie juste que la
	// tentative NE réussit PAS immédiatement, et qu'elle finit par renoncer.
	fini := make(chan bool, 1)
	go func() { fini <- v.prendre(nil) }()

	select {
	case ok := <-fini:
		t.Fatalf("la place de trop a été accordée tout de suite (ok=%v)", ok)
	case <-time.After(100 * time.Millisecond):
		// Attendu : elle patiente.
	}

	// Une place se libère : la requête en attente doit l'obtenir.
	v.rendre()
	select {
	case ok := <-fini:
		if !ok {
			t.Error("une place s'est libérée mais la requête en attente a été refusée")
		}
	case <-time.After(time.Second):
		t.Error("une place s'est libérée et personne ne l'a prise")
	}
}

// Un client qui abandonne ne doit pas continuer d'occuper une place ni la file.
// Sans cela, un navigateur fermé pendant une pointe aggrave la saturation.
func TestVigieRelacheQuandLeClientPart(t *testing.T) {
	v := nouvelleVigie(1)
	if !v.prendre(nil) {
		t.Fatal("la première place devrait être libre")
	}

	quitter := make(chan struct{})
	fini := make(chan bool, 1)
	go func() { fini <- v.prendre(quitter) }()

	time.Sleep(50 * time.Millisecond)
	close(quitter) // le client s'en va

	select {
	case ok := <-fini:
		if ok {
			t.Error("une place a été accordée à un client déjà parti")
		}
	case <-time.After(time.Second):
		t.Error("l'abandon du client n'a pas été pris en compte")
	}
}

// Aucune place ne doit fuir : après N prises et N rendus, tout est libre.
// Une fuite ici saturerait le service progressivement, sans erreur visible
// jusqu'au moment où plus rien ne passe.
func TestVigieNeFuitPas(t *testing.T) {
	const places = 8
	v := nouvelleVigie(places)

	var wg sync.WaitGroup
	var accordees atomic.Int32
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if v.prendre(nil) {
				accordees.Add(1)
				time.Sleep(time.Millisecond)
				v.rendre()
			}
		}()
	}
	wg.Wait()

	if n := v.EnVol(); n != 0 {
		t.Errorf("%d places encore occupées après que tout soit rendu", n)
	}
	if accordees.Load() != 200 {
		t.Errorf("%d requêtes accordées sur 200 : certaines ont été refusées "+
			"alors que le service se libérait", accordees.Load())
	}
}

// Le plafond doit rester sous la limite de connexions de PostgreSQL, sinon il
// ne protège rien. 100 est la valeur par défaut, et il faut garder de la marge
// pour la sonde de santé, les migrations et une session de diagnostic.
func TestPlafondSousLaLimitePostgres(t *testing.T) {
	const maxConnectionsPostgresParDefaut = 100
	if maxEnVol >= maxConnectionsPostgresParDefaut {
		t.Errorf("plafond de %d requêtes en vol pour %d connexions PostgreSQL : "+
			"le plafond ne protège plus rien", maxEnVol, maxConnectionsPostgresParDefaut)
	}
	if reste := maxConnectionsPostgresParDefaut - maxEnVol; reste < 20 {
		t.Errorf("seulement %d connexions de marge : trop peu pour la sonde de "+
			"santé, les migrations et une session psql de diagnostic", reste)
	}
}
