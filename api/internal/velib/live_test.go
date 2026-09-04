//go:build live

// Tests d'intégration contre la VRAIE source Vélib'.
//
// Exclus du build par défaut : une suite qui échoue parce qu'un service tiers
// est en maintenance n'apprend rien à personne, et rend la CI mensongère.
//
//	go test -tags=live ./internal/velib/ -v
//
// Ils servent à deux choses : vérifier que le contrat de la source n'a pas
// bougé, et re-mesurer les chiffres cités dans le README avant le rendu.
package velib

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func liveSnapshot(t *testing.T) Snapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	snap, err := NewClient().Fetch(ctx)
	if err != nil {
		t.Fatalf("source Vélib injoignable : %v", err)
	}
	return snap
}

// Le contrat de la source : si l'un de ces invariants casse, tout le reste est
// faux en silence.
func TestLiveContract(t *testing.T) {
	snap := liveSnapshot(t)
	now := time.Now()

	if len(snap.Stations) < 1000 {
		t.Fatalf("%d stations seulement : le parc réel en compte ~1519", len(snap.Stations))
	}
	t.Logf("parc : %d stations", len(snap.Stations))

	// La jointure a-t-elle vraiment eu lieu ? Une station sans nom signifie que
	// le référentiel n'a pas été fusionné ; une station où tout est à zéro
	// partout signifie que l'état n'a pas été fusionné.
	var named, withState int
	for _, s := range snap.Stations {
		if s.Name != "" {
			named++
		}
		if s.BikesAvailable > 0 || s.DocksAvailable > 0 {
			withState++
		}
	}
	if named != len(snap.Stations) {
		t.Errorf("%d stations sans nom : le référentiel n'est pas joint",
			len(snap.Stations)-named)
	}
	if withState < len(snap.Stations)/2 {
		t.Errorf("seules %d stations portent un état : la jointure a échoué", withState)
	}

	sum := Summarize(snap, now)

	// L'invariant du tableau biscornu : mécaniques + électriques = total.
	// S'il casse, c'est que num_bikes_available_types a changé de forme.
	if sum.BikesMechanical+sum.BikesElectric != sum.BikesTotal {
		t.Errorf("%d mécaniques + %d électriques != %d au total : la forme de "+
			"num_bikes_available_types a probablement changé",
			sum.BikesMechanical, sum.BikesElectric, sum.BikesTotal)
	}

	t.Logf("vélos : %d dont %d électriques et %d mécaniques",
		sum.BikesTotal, sum.BikesElectric, sum.BikesMechanical)
	t.Logf("hors service : %d (%.2f %%)", sum.StationsOutOfOrder, sum.OutOfServicePct)
	t.Logf("vides : %d | pleines : %d", sum.StationsEmpty, sum.StationsFull)
}

// Re-mesure les pièges cités dans le README. À relancer avant le rendu : les
// chiffres du parc bougent, les affirmations du README doivent rester vraies.
func TestLiveTrapsStillHold(t *testing.T) {
	snap := liveSnapshot(t)
	now := time.Now()

	var zeroCap, incoherent, staleOverHour int
	var oldest int64
	for _, s := range snap.Stations {
		if s.Capacity == 0 {
			zeroCap++
			t.Logf("capacité nulle : %s", s.Name)
		}
		if s.BikesAvailable+s.DocksAvailable > s.Capacity {
			incoherent++
		}
		if age := s.AgeSeconds(now); age > 3600 {
			staleOverHour++
			if age > oldest {
				oldest = age
			}
		}
	}

	t.Logf("PIÈGE capacité nulle        : %d stations (division par zéro)", zeroCap)
	t.Logf("PIÈGE bikes+docks>capacity  : %d stations", incoherent)
	t.Logf("PIÈGE remontée > 1 h        : %d stations, la plus vieille à %.1f h",
		staleOverHour, float64(oldest)/3600)

	// Le garde-fou doit tenir sur les vraies stations à capacité nulle.
	for _, s := range snap.Stations {
		if s.Capacity == 0 {
			if _, ok := s.OccupancyRate(); ok {
				t.Errorf("%s : capacité nulle mais taux déclaré exploitable", s.Name)
			}
		}
	}
}

// Les cinq questions de référence, contre les vraies données.
func TestLiveTheFiveQuestions(t *testing.T) {
	snap := liveSnapshot(t)
	now := time.Now()

	t.Run("Q1 vélos électriques", func(t *testing.T) {
		sum := Summarize(snap, now)
		if sum.BikesElectric <= 0 {
			t.Error("aucun vélo électrique : suspect")
		}
		t.Logf("réponse : %d vélos électriques", sum.BikesElectric)
	})

	t.Run("Q2 pourcentage hors service", func(t *testing.T) {
		sum := Summarize(snap, now)
		if sum.OutOfServicePct < 0 || sum.OutOfServicePct > 100 {
			t.Errorf("pourcentage aberrant : %v", sum.OutOfServicePct)
		}
		t.Logf("réponse : %.2f %% (%d stations)", sum.OutOfServicePct, sum.StationsOutOfOrder)
	})

	t.Run("Q3 top 5 bornes libres", func(t *testing.T) {
		r := Rank(snap, MetricDocksAvailable, 5, false, now)
		if len(r.Stations) != 5 {
			t.Fatalf("%d stations, attendu 5", len(r.Stations))
		}
		for _, s := range r.Stations {
			t.Logf("  %3d bornes libres — %s", s.DocksAvailable, s.Name)
		}
	})

	t.Run("Q4 station Benjamin Godard", func(t *testing.T) {
		d := FindStations(snap, "Benjamin Godard", now)
		if d.MatchCount == 0 {
			t.Fatal("station introuvable alors qu'elle existe dans le parc réel")
		}
		t.Logf("réponse : %s — %d vélos",
			d.Stations[0].Name, d.Stations[0].BikesAvailable)
	})

	t.Run("Q5 stations vides", func(t *testing.T) {
		c := Count(snap, FilterEmpty, MaxSampleSize, now)
		t.Logf("réponse : %d stations vides (%.2f %%), échantillon de %d",
			c.Total, c.Pct, len(c.Sample))
		if c.Total > MaxSampleSize && !c.Truncated {
			t.Error("troncature non signalée")
		}
	})
}

// LA mesure qui justifie toute l'architecture : ce que pèse une réponse d'outil
// face aux données brutes.
func TestLiveContextReduction(t *testing.T) {
	snap := liveSnapshot(t)
	now := time.Now()

	// Ce qu'un outil naïf renverrait : le parc entier.
	naive, err := json.Marshal(snap.Stations)
	if err != nil {
		t.Fatalf("sérialisation : %v", err)
	}

	outputs := map[string]any{
		"network_summary": Summarize(snap, now),
		"rank_stations":   Rank(snap, MetricDocksAvailable, 5, false, now),
		"count_stations":  Count(snap, FilterEmpty, MaxSampleSize, now),
		"find_station":    FindStations(snap, "Benjamin Godard", now),
	}

	t.Logf("parc brut sérialisé : %d octets", len(naive))
	var worst int
	for name, o := range outputs {
		b, _ := json.Marshal(o)
		if len(b) > worst {
			worst = len(b)
		}
		t.Logf("  %-16s %5d octets   (réduction %dx)",
			name, len(b), len(naive)/max(len(b), 1))
	}

	// L'invariant : même la plus grosse réponse doit rester deux ordres de
	// grandeur sous le parc brut.
	if worst*100 > len(naive) {
		t.Errorf("la plus grosse sortie fait %d octets pour %d bruts : "+
			"la réduction est insuffisante", worst, len(naive))
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
