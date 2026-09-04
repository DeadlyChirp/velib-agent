package httpapi

import (
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Horloge contrôlée : un test de limite de débit qui attend vraiment quinze
// minutes est un test que personne ne relance.
func limiteurDeTest() (*limiteur, func(time.Duration)) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	horloge := t0
	l := nouveauLimiteur()
	l.maintenant = func() time.Time { return horloge }
	return l, func(d time.Duration) { horloge = horloge.Add(d) }
}

func TestRafalePuisBlocage(t *testing.T) {
	l, _ := limiteurDeTest()

	for i := 0; i < debitRafale; i++ {
		if ok, _ := l.autorise("alice"); !ok {
			t.Fatalf("requête %d refusée alors que la rafale en autorise %d",
				i+1, debitRafale)
		}
	}
	ok, attendre := l.autorise("alice")
	if ok {
		t.Error("une requête de trop passe : la rafale n'est pas bornée")
	}
	if attendre <= 0 {
		t.Error("aucun délai indiqué : le client ne sait pas quand réessayer")
	}
}

func TestLeSeauSeRemplitAvecLeTemps(t *testing.T) {
	l, avancer := limiteurDeTest()

	for i := 0; i < debitRafale; i++ {
		l.autorise("bob")
	}
	if ok, _ := l.autorise("bob"); ok {
		t.Fatal("le seau devrait être vide")
	}

	// Une minute regagne debitParMinute jetons, donc largement de quoi repartir.
	avancer(time.Minute)
	if ok, _ := l.autorise("bob"); !ok {
		t.Error("après une minute le client reste bloqué : le seau ne se remplit pas")
	}
}

// Le seau ne doit pas déborder : attendre une heure ne donne pas droit à
// soixante fois le débit d'un coup.
func TestLeSeauNeDeborda(t *testing.T) {
	l, avancer := limiteurDeTest()
	l.autorise("carol")
	avancer(time.Hour)

	passees := 0
	for i := 0; i < debitRafale*10; i++ {
		if ok, _ := l.autorise("carol"); ok {
			passees++
		}
	}
	if passees > debitRafale {
		t.Errorf("%d requêtes passées d'affilée, plafond %d : le seau déborde",
			passees, debitRafale)
	}
}

// Deux clients ne partagent pas leur budget. Sans cela, un utilisateur bruyant
// bloquerait tous les autres — la limite deviendrait elle-même le déni de
// service qu'elle prétend éviter.
func TestClientsIndependants(t *testing.T) {
	l, _ := limiteurDeTest()

	for i := 0; i < debitRafale; i++ {
		l.autorise("bruyant")
	}
	if ok, _ := l.autorise("bruyant"); ok {
		t.Fatal("le client bruyant devrait être bloqué")
	}
	if ok, _ := l.autorise("tranquille"); !ok {
		t.Error("un client innocent est bloqué par le budget d'un autre")
	}
}

// La table des clients doit se vider, sinon c'est une fuite mémoire lente :
// invisible en développement, visible après quelques semaines de production.
func TestLesClientsOubliesSontPurges(t *testing.T) {
	l, avancer := limiteurDeTest()

	for i := 0; i < 500; i++ {
		l.autorise("passant-" + string(rune('a'+i%26)) + string(rune('a'+i/26)))
	}
	avant := len(l.seaux)
	if avant < 100 {
		t.Fatalf("seulement %d clients enregistrés, le test ne prouve rien", avant)
	}

	avancer(debitOubli + time.Minute)
	l.autorise("declencheur") // le nettoyage se fait à l'occasion d'un appel

	if len(l.seaux) > 5 {
		t.Errorf("%d clients encore en mémoire après expiration (avant : %d)",
			len(l.seaux), avant)
	}
}

// Le limiteur est appelé depuis les goroutines de requête : une course ici
// corromprait la table et ne se verrait qu'en production.
func TestLimiteurConcurrent(t *testing.T) {
	l := nouveauLimiteur()
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			l.autorise("client-" + string(rune('a'+n%7)))
		}(i)
	}
	wg.Wait()
}

// L'identification tombe sur l'adresse IP quand aucune identité applicative
// n'est fournie, et X-Forwarded-For n'est jamais lu : falsifiable d'une ligne,
// il donnerait une clé différente à chaque requête et annulerait la limite.
func TestIdentificationDuClient(t *testing.T) {
	cas := []struct {
		nom     string
		entetes map[string]string
		distant string
		attendu string
	}{
		{"identité applicative", map[string]string{"X-User-ID": "alice"}, "10.0.0.1:5000", "u:alice"},
		{"sans identité", nil, "10.0.0.1:5000", "ip:10.0.0.1"},
		{"identité par défaut ignorée", map[string]string{"X-User-ID": defaultUserID}, "10.0.0.2:5000", "ip:10.0.0.2"},
		{"X-Forwarded-For ignoré", map[string]string{"X-Forwarded-For": "1.2.3.4"}, "10.0.0.3:5000", "ip:10.0.0.3"},
	}
	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/conversations/x/messages", nil)
			r.RemoteAddr = c.distant
			for k, v := range c.entetes {
				r.Header.Set(k, v)
			}
			if got := clientDe(r); got != c.attendu {
				t.Errorf("clientDe = %q, attendu %q", got, c.attendu)
			}
		})
	}
}
