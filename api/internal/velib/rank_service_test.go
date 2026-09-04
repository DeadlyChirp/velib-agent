package velib

import (
	"strings"
	"testing"
)

// Ces tests verrouillent la correction du défaut le plus grave trouvé pendant
// l'audit : Rank était la seule agrégation à ignorer OutOfService.
//
// Sur les données réelles du 04/09, le top 3 par bornes libres était
// intégralement composé de stations hors service — « Station Tour de France »
// (200 bornes, donnée de 955 h), « Championnats d'Europe de Natation » (200
// bornes, 449 h) et « Hippodrome de Vincennes » (97 bornes, 295 jours). Trois
// endroits où l'on ne peut rendre aucun vélo, présentés comme les meilleures
// réponses à la question 3 de référence.
//
// La fixture d'origine ne pouvait pas attraper ce cas : sa station hors service
// n'avait que 17 bornes, jamais assez pour atteindre le sommet. Un piège présent
// dans le jeu de test mais hors de portée de l'assertion ne protège de rien.

// staleFixture place une station hors service EN TÊTE de la métrique, ce qui est
// exactement la configuration du parc réel.
func staleFixture() Snapshot {
	s := fixture()
	for i := range s.Stations {
		if s.Stations[i].Name == "Station En Panne" {
			// 200 bornes libres, comme les vraies stations fantômes : elle domine
			// désormais la métrique et DOIT être écartée.
			s.Stations[i].DocksAvailable = 200
			s.Stations[i].Capacity = 210
		}
	}
	return s
}

func TestRankExcludesOutOfServiceStations(t *testing.T) {
	got := Rank(staleFixture(), MetricDocksAvailable, 3, false, refTime)

	for _, st := range got.Stations {
		if st.Name == "Station En Panne" {
			t.Fatalf("une station hors service domine le classement avec %d bornes "+
				"libres alors qu'elle ne reprend aucun vélo", st.DocksAvailable)
		}
		if st.OutOfService {
			t.Errorf("station hors service dans le classement : %s", st.Name)
		}
	}

	// Le sommet doit être la meilleure station RÉELLEMENT en service.
	if len(got.Stations) == 0 || got.Stations[0].DocksAvailable != 30 {
		t.Errorf("première station à %v bornes, attendu 30 (Roland Barthes)",
			got.Stations)
	}
}

// L'exclusion doit être annoncée au modèle : une réponse amputée sans
// explication est une réponse qu'il présentera comme complète.
func TestRankAnnouncesExclusions(t *testing.T) {
	got := Rank(staleFixture(), MetricDocksAvailable, 3, false, refTime)

	if !strings.Contains(got.Note, "hors service") {
		t.Errorf("le modèle n'est pas informé de l'exclusion, note = %q", got.Note)
	}
}

// capacity décrit la taille physique de la station, pas le service rendu :
// une station fermée reste la plus grande station. Le filtre ne doit donc PAS
// s'y appliquer.
func TestRankKeepsOutOfServiceForCapacity(t *testing.T) {
	got := Rank(staleFixture(), MetricCapacity, 3, false, refTime)

	found := false
	for _, st := range got.Stations {
		if st.Name == "Station En Panne" {
			found = true
		}
	}
	if !found {
		t.Error("le classement par capacité doit conserver les stations hors " +
			"service : la capacité est une propriété physique, pas un service")
	}
}

// Le champ Note portait déjà « limite ramenée à 20 ». Une affectation directe
// aurait effacé ce signal au profit du message d'exclusion.
func TestRankNotesAccumulateWithoutOverwriting(t *testing.T) {
	got := Rank(staleFixture(), MetricDocksAvailable, 500, false, refTime)

	if !strings.Contains(got.Note, "20") {
		t.Errorf("le signal de bridage a disparu, note = %q", got.Note)
	}
	if !strings.Contains(got.Note, "hors service") {
		t.Errorf("le signal d'exclusion a disparu, note = %q", got.Note)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// La recherche large : compter les correspondances réelles, pas les renvoyées
// ─────────────────────────────────────────────────────────────────────────────

// Sur le parc réel, « place » ou « gare » correspondent à des dizaines de
// stations. Sans distinction entre résultats montrés et correspondances
// réelles, le modèle annonce trois stations comme s'il n'en existait que trois.
func TestFindStationsReportsRealTotalNotJustShown(t *testing.T) {
	got := FindStations(fixture(), "station", refTime)

	if got.MatchCount > MaxCandidates {
		t.Errorf("%d stations renvoyées, la borne de %d n'est pas appliquée",
			got.MatchCount, MaxCandidates)
	}
	if got.TotalMatches < got.MatchCount {
		t.Errorf("total_matches (%d) inférieur au nombre renvoyé (%d) : incohérent",
			got.TotalMatches, got.MatchCount)
	}
	if got.TotalMatches > got.MatchCount && !got.Truncated {
		t.Error("troncature non signalée alors que des correspondances sont cachées")
	}
	if got.Truncated && !strings.Contains(got.Note, "au total") {
		t.Errorf("la note ne dit pas combien de stations existent vraiment : %q",
			got.Note)
	}
}

// Une recherche qui tient dans la borne ne doit rien signaler.
func TestFindStationsNotTruncatedWhenItFits(t *testing.T) {
	got := FindStations(fixture(), "Benjamin Godard", refTime)

	if got.Truncated {
		t.Error("une seule correspondance ne doit pas être marquée tronquée")
	}
	if got.TotalMatches != got.MatchCount {
		t.Errorf("total_matches=%d et match_count=%d devraient être égaux",
			got.TotalMatches, got.MatchCount)
	}
}
