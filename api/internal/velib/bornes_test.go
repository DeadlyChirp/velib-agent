package velib

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// L'INVARIANT QUI PROTÈGE LA FENÊTRE DE CONTEXTE.
//
// La question « et si le parc faisait un million de stations ? » n'a pas pour
// réponse « on optimise ». Elle a pour réponse : la taille de ce qu'un outil
// renvoie ne doit PAS dépendre du nombre de stations.
//
// C'est ce qui rend l'énumération impossible par construction. Un utilisateur
// qui demande « liste-moi toutes les stations » ne peut pas faire exploser le
// contexte, parce qu'aucun outil ne sait renvoyer une liste complète — ils
// renvoient un compte, un classement plafonné, ou trois candidats.
//
// Sans ce test, la protection tient à la discipline de celui qui ajoutera le
// prochain outil. Avec, elle est vérifiée.
func TestTailleDeSortieIndependanteDuNombreDeStations(t *testing.T) {
	tailles := []int{1_519, 151_900, 1_519_000}

	// Une sortie d'outil doit rester sous ce plafond quel que soit le parc.
	// 4 Ko représente environ mille jetons : au-delà, un seul appel d'outil
	// mangerait une part visible de la fenêtre.
	const plafond = 4096

	mesures := map[string][]int{}

	for _, n := range tailles {
		snap := parcDe(n)
		now := time.Now()

		sorties := map[string]any{
			"network_summary": Summarize(snap, now),
			"rank_stations":   Rank(snap, MetricBikesAvailable, MaxRankLimit, false, now),
			"count_stations":  Count(snap, FilterEmpty, MaxSampleSize, now),
			"find_station":    FindStations(snap, "Quartier 42", now),
		}

		for nom, sortie := range sorties {
			b, err := json.Marshal(sortie)
			if err != nil {
				t.Fatalf("%s à %d stations : sérialisation : %v", nom, n, err)
			}
			if len(b) > plafond {
				t.Errorf("%s à %d stations : %d octets, plafond %d",
					nom, n, len(b), plafond)
			}
			mesures[nom] = append(mesures[nom], len(b))
		}
	}

	// Le cœur du test : entre le plus petit et le plus grand parc, les données
	// sont multipliées par mille. La sortie ne doit pas suivre.
	for nom, tailles := range mesures {
		petit, grand := tailles[0], tailles[len(tailles)-1]
		t.Logf("%-16s %5d → %5d octets pour un parc ×1000", nom, petit, grand)

		// On tolère un facteur 2 : les nombres gagnent des chiffres, les noms
		// de stations sont plus longs. Un facteur 10 signalerait une sortie qui
		// grandit avec le parc, donc une énumération déguisée.
		if grand > petit*2 {
			t.Errorf("%s : la sortie a été multipliée par %.1f alors que le parc "+
				"a été multiplié par 1000 — la sortie dépend du nombre de stations",
				nom, float64(grand)/float64(petit))
		}
	}
}

// Le plafond de classement est appliqué CÔTÉ SERVEUR et non négociable. Un
// modèle qui demande vingt mille stations doit en recevoir vingt.
func TestPlafondsNonNegociables(t *testing.T) {
	snap := parcDe(50_000)
	now := time.Now()

	t.Run("rank ignore une limite absurde", func(t *testing.T) {
		for _, demande := range []int{100, 10_000, 1_000_000, -1, 0} {
			r := Rank(snap, MetricBikesAvailable, demande, false, now)
			if len(r.Stations) > MaxRankLimit {
				t.Errorf("limite demandée %d : %d stations rendues, plafond %d",
					demande, len(r.Stations), MaxRankLimit)
			}
		}
	})

	t.Run("count ignore un échantillon absurde", func(t *testing.T) {
		for _, demande := range []int{50, 10_000, 1_000_000, -1} {
			c := Count(snap, FilterEmpty, demande, now)
			if len(c.Sample) > MaxSampleSize {
				t.Errorf("échantillon demandé %d : %d exemples rendus, plafond %d",
					demande, len(c.Sample), MaxSampleSize)
			}
			// Le TOTAL, lui, reste exact : c'est la liste qu'on borne, pas le
			// comptage. Répondre « beaucoup » serait inutile.
			if c.Total == 0 && len(c.Sample) > 0 {
				t.Error("des exemples sans total : le comptage a été perdu")
			}
		}
	})

	t.Run("find plafonne les candidats", func(t *testing.T) {
		// « Station » correspond à TOUTES les stations du jeu de test.
		d := FindStations(snap, "Station", now)
		if len(d.Stations) > MaxCandidates {
			t.Errorf("%d candidats rendus, plafond %d", len(d.Stations), MaxCandidates)
		}
		if d.TotalMatches < 1000 {
			t.Errorf("total = %d : le compte doit rester exact même tronqué",
				d.TotalMatches)
		}
		if !d.Truncated {
			t.Error("liste tronquée sans que Truncated soit posé : le modèle " +
				"croira avoir vu tous les résultats")
		}
	})
}

// Un nom de station hostile ne doit pas pouvoir inonder le contexte.
//
// Les noms viennent d'une source externe : ils entrent dans le contexte du
// modèle comme du texte, via les sorties d'outils. Un nom de 100 000
// caractères — accident de saisie chez l'opérateur, ou pire — ferait sauter le
// plafond de sortie à lui seul.
func TestNomDeStationDemesureNInondePasLeContexte(t *testing.T) {
	snap := parcDe(100)
	snap.Stations[0].Name = strings.Repeat("Station très longue ", 5_000)
	snap.Stations[0].searchKey = normalize(snap.Stations[0].Name)

	now := time.Now()
	for nom, sortie := range map[string]any{
		"rank_stations":  Rank(snap, MetricBikesAvailable, MaxRankLimit, false, now),
		"count_stations": Count(snap, FilterEmpty, MaxSampleSize, now),
		"find_station":   FindStations(snap, "Station très longue", now),
	} {
		b, _ := json.Marshal(sortie)
		if len(b) > 8192 {
			t.Errorf("%s : %d octets à cause d'un seul nom démesuré", nom, len(b))
		}
	}
}
