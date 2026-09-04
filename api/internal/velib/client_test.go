package velib

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Le client est la frontière avec le monde extérieur : réseau qui tombe, source
// qui répond 500, JSON tronqué en plein transfert. C'est exactement là où les
// bugs vivent, et c'était la seule partie du projet sans aucun test hors réseau.
//
// On simule la source avec httptest plutôt que d'appeler la vraie API : un test
// qui rougit parce qu'un service public redémarre n'apprend rien à personne, et
// on ne peut pas demander à Smovengo de renvoyer un 500 à la demande.

const jsonInfo = `{"lastUpdatedOther":1,"ttl":3600,"data":{"stations":[
  {"station_id":1,"name":"Alpha","stationCode":"1001","capacity":30},
  {"station_id":2,"name":"Beta","stationCode":"1002","capacity":20}]}}`

const jsonStatus = `{"lastUpdatedOther":1,"ttl":3600,"data":{"stations":[
  {"station_id":1,"num_bikes_available":5,"num_docks_available":25,"is_installed":1,
   "is_renting":1,"is_returning":1,"last_reported":1,
   "num_bikes_available_types":[{"mechanical":3},{"ebike":2}]},
  {"station_id":2,"num_bikes_available":0,"num_docks_available":20,"is_installed":1,
   "is_renting":1,"is_returning":1,"last_reported":1,
   "num_bikes_available_types":[{"mechanical":0},{"ebike":0}]}]}}`

// source monte un serveur qui répond selon les fonctions fournies.
func source(t *testing.T, info, statut http.HandlerFunc) *Client {
	t.Helper()
	srvInfo := httptest.NewServer(info)
	srvStatut := httptest.NewServer(statut)
	t.Cleanup(srvInfo.Close)
	t.Cleanup(srvStatut.Close)
	return NewClient(
		WithInformationURL(srvInfo.URL),
		WithStatusURL(srvStatut.URL),
		WithMaxAttempts(3),
		WithHTTPClient(&http.Client{Timeout: 3 * time.Second}),
	)
}

func repond(corps string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, corps)
	}
}

func TestFetchNominal(t *testing.T) {
	c := source(t, repond(jsonInfo), repond(jsonStatus))

	snap, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch : %v", err)
	}
	if len(snap.Stations) != 2 {
		t.Fatalf("%d stations, attendu 2", len(snap.Stations))
	}
	if snap.Stations[0].Name != "Alpha" || snap.Stations[0].BikesAvailable != 5 {
		t.Errorf("jointure incorrecte : %+v", snap.Stations[0])
	}
	if snap.Stations[0].Ebikes != 2 {
		t.Errorf("vélos électriques = %d, attendu 2", snap.Stations[0].Ebikes)
	}
	if snap.FetchedAt.IsZero() {
		t.Error("FetchedAt non renseigné : le cache ne saura pas dater la donnée")
	}
}

// L'en-tête d'identification part bien : une source publique gratuite a le
// droit de savoir qui l'interroge, et c'est ce qui permet d'être contacté
// plutôt que bloqué en cas d'usage excessif.
func TestFetchEnvoieUnUserAgent(t *testing.T) {
	var vu atomic.Value
	h := func(w http.ResponseWriter, r *http.Request) {
		vu.Store(r.Header.Get("User-Agent"))
		fmt.Fprint(w, jsonInfo)
	}
	c := source(t, h, repond(jsonStatus))
	_, _ = c.Fetch(context.Background())

	ua, _ := vu.Load().(string)
	if !strings.Contains(ua, "velib-agent") {
		t.Errorf("User-Agent = %q, devrait identifier le service", ua)
	}
}

// Un 500 est passager : on retente. C'est le comportement qui fait la
// différence entre un service qui hoquette et un service qui tombe.
func TestRetenteSurCinqCents(t *testing.T) {
	var appels atomic.Int32
	h := func(w http.ResponseWriter, r *http.Request) {
		if appels.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, jsonInfo)
	}
	c := source(t, h, repond(jsonStatus))

	snap, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatalf("le client abandonne alors que la source repart : %v", err)
	}
	if len(snap.Stations) != 2 {
		t.Errorf("%d stations après reprise", len(snap.Stations))
	}
	if n := appels.Load(); n != 3 {
		t.Errorf("%d appels, attendu 3 : la reprise ne suit pas le plafond", n)
	}
}

func TestRetenteSurQuatreCentVingtNeuf(t *testing.T) {
	var appels atomic.Int32
	h := func(w http.ResponseWriter, r *http.Request) {
		if appels.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, jsonInfo)
	}
	c := source(t, h, repond(jsonStatus))

	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatalf("un 429 devrait être retenté : %v", err)
	}
}

// Un 404 ne se répare pas en insistant. Retenter trois fois une URL fausse
// ajoute deux secondes d'attente pour rien, et brouille le diagnostic.
func TestNeRetentePasUnQuatreCentQuatre(t *testing.T) {
	var appels atomic.Int32
	h := func(w http.ResponseWriter, r *http.Request) {
		appels.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}
	c := source(t, h, repond(jsonStatus))

	if _, err := c.Fetch(context.Background()); err == nil {
		t.Fatal("un 404 devrait remonter une erreur")
	}
	if n := appels.Load(); n != 1 {
		t.Errorf("%d appels sur un 404, attendu 1 : erreur non rejouable retentée", n)
	}
}

// Le cas le plus vicieux : la source répond 200 avec du JSON tronqué. Sans
// vérification, on obtient un parc vide qui ressemble à un parc légitimement
// vide, et le service annonce zéro station en toute confiance.
func TestJSONTronqueRemonteUneErreur(t *testing.T) {
	c := source(t, repond(`{"data":{"stations":[{"station_id":1,`), repond(jsonStatus))

	if _, err := c.Fetch(context.Background()); err == nil {
		t.Fatal("du JSON tronqué doit produire une erreur, pas un parc vide")
	}
}

func TestCorpsVideRemonteUneErreur(t *testing.T) {
	c := source(t, repond(""), repond(jsonStatus))

	if _, err := c.Fetch(context.Background()); err == nil {
		t.Fatal("un corps vide doit produire une erreur")
	}
}

// Les deux flux partent en parallèle, mais l'échec de l'un doit faire échouer
// l'ensemble : un parc sans statut est un parc dont on ne sait rien.
func TestUnSeulFluxEnEchecFaitEchouerLeTout(t *testing.T) {
	c := source(t, repond(jsonInfo), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	if _, err := c.Fetch(context.Background()); err == nil {
		t.Fatal("statut injoignable : le parc ne doit pas être rendu à moitié")
	}
}

// Une annulation doit remonter tout de suite. Sans cela, un client qui ferme
// son navigateur laisse le serveur retenter trois fois pour personne.
func TestContexteAnnuleRemonteImmediatement(t *testing.T) {
	lent := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
	}
	c := source(t, lent, lent)

	ctx, annuler := context.WithCancel(context.Background())
	go func() { time.Sleep(80 * time.Millisecond); annuler() }()

	debut := time.Now()
	_, err := c.Fetch(ctx)
	ecoule := time.Since(debut)

	if err == nil {
		t.Fatal("une annulation doit produire une erreur")
	}
	if ecoule > 2*time.Second {
		t.Errorf("annulation traitée en %v : le client continue de retenter", ecoule)
	}
}

// Le délai de garde protège d'une source qui accepte la connexion puis se tait.
// C'est la panne la plus coûteuse : rien n'échoue, tout attend.
func TestDelaiDeGarde(t *testing.T) {
	muet := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(10 * time.Second):
		case <-r.Context().Done():
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(muet))
	t.Cleanup(srv.Close)

	c := NewClient(
		WithInformationURL(srv.URL), WithStatusURL(srv.URL),
		WithMaxAttempts(1),
		WithHTTPClient(&http.Client{Timeout: 200 * time.Millisecond}),
	)

	debut := time.Now()
	if _, err := c.Fetch(context.Background()); err == nil {
		t.Fatal("une source muette doit produire une erreur")
	}
	if ecoule := time.Since(debut); ecoule > 2*time.Second {
		t.Errorf("attendu %v avant d'abandonner : le plafond de temps ne s'applique pas", ecoule)
	}
}

// Une station présente dans le référentiel mais absente du statut doit rester
// visible, avec des compteurs à zéro. La faire disparaître ferait mentir le
// total du réseau.
func TestStationSansStatutResteVisible(t *testing.T) {
	statutPartiel := `{"lastUpdatedOther":1,"ttl":3600,"data":{"stations":[
	  {"station_id":1,"num_bikes_available":5,"num_docks_available":25,"is_installed":1,
	   "is_renting":1,"is_returning":1,"last_reported":1,
	   "num_bikes_available_types":[{"mechanical":5}]}]}}`
	c := source(t, repond(jsonInfo), repond(statutPartiel))

	snap, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch : %v", err)
	}
	if len(snap.Stations) != 2 {
		t.Fatalf("%d stations, attendu 2 : une station sans statut a disparu", len(snap.Stations))
	}
}
