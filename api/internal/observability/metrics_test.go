package observability

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// Un collecteur vide doit rendre des zéros, pas paniquer. C'est l'état dans
// lequel /api/metrics est interrogée juste après un démarrage — donc à chaque
// déploiement, et par toute sonde de supervision.
func TestSnapshotVideNePaniquePas(t *testing.T) {
	s := New().Snapshot()

	if s.Turns.Total != 0 || s.Turns.AvgMs != 0 || s.Turns.ErrorRatePct != 0 {
		t.Errorf("un collecteur vide devrait rendre des zéros : %+v", s.Turns)
	}
	if s.Cache.HitRatePct != 0 {
		t.Errorf("taux de cache sur zéro appel = %v, attendu 0", s.Cache.HitRatePct)
	}
	if len(s.Tools) != 0 {
		t.Errorf("aucun outil appelé, pourtant %d entrées", len(s.Tools))
	}
}

// pct est le seul endroit où une division peut rencontrer un zéro.
func TestPct(t *testing.T) {
	cas := []struct {
		part, total int64
		attendu     float64
	}{
		{0, 0, 0}, // division par zéro : le cas qui compte
		{1, 0, 0},
		{1, 2, 50},
		{1, 3, 33.33}, // arrondi à deux décimales
		{2, 3, 66.67},
		{3, 3, 100},
	}
	for _, c := range cas {
		if got := pct(c.part, c.total); got != c.attendu {
			t.Errorf("pct(%d, %d) = %v, attendu %v", c.part, c.total, got, c.attendu)
		}
	}
}

// L'historique récent est borné : sans cela, un service qui tourne des semaines
// accumule un tour par question jusqu'à saturer la mémoire. C'est une fuite qui
// ne se voit qu'en production, et tard.
func TestHistoriqueRecentBorne(t *testing.T) {
	m := New()
	for i := 0; i < maxRecentTurns*5; i++ {
		m.RecordTurn(TurnRecord{At: time.Now(), Status: "ok", DurationMs: 10})
	}

	s := m.Snapshot()
	if len(s.Recent) > maxRecentTurns {
		t.Errorf("%d tours conservés, plafond %d : fuite mémoire",
			len(s.Recent), maxRecentTurns)
	}
	// Le total, lui, ne doit PAS être borné : c'est un compteur, pas un tampon.
	if s.Turns.Total != int64(maxRecentTurns*5) {
		t.Errorf("total = %d, attendu %d : le compteur ne doit pas être tronqué",
			s.Turns.Total, maxRecentTurns*5)
	}
}

func TestMoyennesEtTauxDErreur(t *testing.T) {
	m := New()
	m.RecordTurn(TurnRecord{Status: "ok", DurationMs: 100, PromptTokens: 10, CompletionTokens: 5})
	m.RecordTurn(TurnRecord{Status: "ok", DurationMs: 300, PromptTokens: 20, CompletionTokens: 5})
	m.RecordTurn(TurnRecord{Status: "error", DurationMs: 200})

	s := m.Snapshot()
	if s.Turns.Total != 3 {
		t.Errorf("total = %d, attendu 3", s.Turns.Total)
	}
	if s.Turns.AvgMs != 200 { // (100+300+200)/3
		t.Errorf("moyenne = %d ms, attendu 200", s.Turns.AvgMs)
	}
	if s.Turns.ErrorRatePct != 33.33 { // 1 sur 3
		t.Errorf("taux d'erreur = %v, attendu 33.33", s.Turns.ErrorRatePct)
	}
	if s.Turns.TotalTokens != 40 {
		t.Errorf("jetons = %d, attendu 40", s.Turns.TotalTokens)
	}
}

func TestCacheHitsEtStale(t *testing.T) {
	m := New()
	m.RecordCacheHit()
	m.RecordCacheHit()
	m.RecordCacheHit()
	m.RecordCacheRefresh(1519, nil)
	m.RecordCacheRefresh(0, errors.New("source injoignable"))
	m.RecordStaleServed()

	s := m.Snapshot()
	if s.Cache.Hits != 3 {
		t.Errorf("hits = %d, attendu 3", s.Cache.Hits)
	}
	if s.Cache.Refreshes != 2 {
		t.Errorf("rafraîchissements = %d, attendu 2", s.Cache.Refreshes)
	}
	if s.Cache.RefreshErrors != 1 {
		t.Errorf("erreurs = %d, attendu 1", s.Cache.RefreshErrors)
	}
	if s.Cache.StaleServed != 1 {
		t.Errorf("périmé servi = %d, attendu 1", s.Cache.StaleServed)
	}
	// Un rafraîchissement en échec ne doit PAS écraser le nombre de stations
	// connu : sinon le tableau de bord annonce zéro station alors que le
	// service répond correctement avec la donnée précédente.
	if s.Cache.Stations != 1519 {
		t.Errorf("stations = %d, attendu 1519 : un échec a écrasé la valeur connue",
			s.Cache.Stations)
	}
}

func TestOutilsAgreges(t *testing.T) {
	m := New()
	m.RecordTool("find_station", 10*time.Millisecond, false)
	m.RecordTool("find_station", 30*time.Millisecond, false)
	m.RecordTool("find_station", 20*time.Millisecond, true)
	m.RecordTool("rank_stations", 5*time.Millisecond, false)

	s := m.Snapshot()
	if len(s.Tools) != 2 {
		t.Fatalf("%d outils, attendu 2 : %+v", len(s.Tools), s.Tools)
	}
	var trouve bool
	for _, o := range s.Tools {
		if o.Name != "find_station" {
			continue
		}
		trouve = true
		if o.Calls != 3 {
			t.Errorf("appels = %d, attendu 3", o.Calls)
		}
		if o.Errors != 1 {
			t.Errorf("erreurs = %d, attendu 1", o.Errors)
		}
		if o.AvgMs != 20 { // (10+30+20)/3
			t.Errorf("moyenne = %d ms, attendu 20", o.AvgMs)
		}
		if o.MaxMs != 30 {
			t.Errorf("max = %d ms, attendu 30", o.MaxMs)
		}
	}
	if !trouve {
		t.Error("find_station absent du relevé")
	}
}

// Le collecteur est écrit par les goroutines de requête pendant que
// /api/metrics le lit. C'est exactement la forme d'accès concurrent où une
// course ne se manifeste qu'en production, de façon intermittente.
//
// À lancer avec -race, ce que fait la CI.
func TestAccesConcurrent(t *testing.T) {
	m := New()
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); m.RecordTurn(TurnRecord{Status: "ok", DurationMs: 10}) }()
		go func() { defer wg.Done(); m.RecordTool("find_station", time.Millisecond, false) }()
		go func() { defer wg.Done(); _ = m.Snapshot() }()
	}
	wg.Wait()

	if s := m.Snapshot(); s.Turns.Total != 50 {
		t.Errorf("total = %d après 50 écritures concurrentes, attendu 50", s.Turns.Total)
	}
}

// Snapshot doit rendre une COPIE. Si elle partageait la tranche interne,
// l'appelant qui la sérialise pendant qu'une requête écrit lirait une valeur en
// cours de modification — et le détecteur de course ne le verrait pas
// forcément, parce que la faute est de conception, pas de synchronisation.
func TestSnapshotEstUneCopie(t *testing.T) {
	m := New()
	m.RecordTurn(TurnRecord{Status: "ok", ConversationID: "origine"})

	s := m.Snapshot()
	if len(s.Recent) == 0 {
		t.Fatal("aucun tour dans le relevé")
	}
	s.Recent[0].ConversationID = "modifié par l'appelant"

	if apres := m.Snapshot(); apres.Recent[0].ConversationID != "origine" {
		t.Error("modifier le relevé a modifié l'état interne du collecteur")
	}
}
