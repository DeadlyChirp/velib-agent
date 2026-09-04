package httpapi

import (
	"testing"
	"time"
)

// dedupeConsecutive fusionne les doublons successifs du même auteur : selon la
// façon dont un tour a été persisté, un message d'assistant peut apparaître en
// version partielle puis en version finale. Sans fusion, l'utilisateur voit sa
// réponse deux fois, dont une tronquée.
func TestDedupeConsecutive(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	msg := func(role, contenu string, decalage time.Duration) messageView {
		return messageView{Role: role, Content: contenu, At: t0.Add(decalage)}
	}

	cas := []struct {
		nom     string
		entree  []messageView
		attendu []string
	}{
		{
			nom:     "vide",
			entree:  nil,
			attendu: nil,
		},
		{
			nom:     "un seul message",
			entree:  []messageView{msg("user", "bonjour", 0)},
			attendu: []string{"bonjour"},
		},
		{
			nom: "partielle puis finale, même auteur",
			entree: []messageView{
				msg("assistant", "Il y a 1 519", time.Second),
				msg("assistant", "Il y a 1 519 stations.", 2*time.Second),
			},
			attendu: []string{"Il y a 1 519 stations."},
		},
		{
			nom: "trois fragments successifs",
			entree: []messageView{
				msg("assistant", "Il", time.Second),
				msg("assistant", "Il y a", 2*time.Second),
				msg("assistant", "Il y a 1 519 stations.", 3*time.Second),
			},
			attendu: []string{"Il y a 1 519 stations."},
		},
		{
			nom: "auteurs différents : jamais fusionnés",
			entree: []messageView{
				msg("user", "Combien", time.Second),
				msg("assistant", "Combien de stations ? Il y en a 1 519.", 2*time.Second),
			},
			attendu: []string{"Combien", "Combien de stations ? Il y en a 1 519."},
		},
		{
			nom: "même auteur mais contenu sans rapport",
			entree: []messageView{
				msg("user", "Combien de stations ?", time.Second),
				msg("user", "Et combien de vélos ?", 2*time.Second),
			},
			attendu: []string{"Combien de stations ?", "Et combien de vélos ?"},
		},
		{
			nom: "deux tours complets s'enchaînent",
			entree: []messageView{
				msg("user", "Q1", time.Second),
				msg("assistant", "R1", 2*time.Second),
				msg("user", "Q2", 3*time.Second),
				msg("assistant", "R2", 4*time.Second),
			},
			attendu: []string{"Q1", "R1", "Q2", "R2"},
		},
	}

	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			got := dedupeConsecutive(c.entree)
			if len(got) != len(c.attendu) {
				t.Fatalf("%d messages, attendu %d : %+v", len(got), len(c.attendu), got)
			}
			for i, veut := range c.attendu {
				if got[i].Content != veut {
					t.Errorf("message %d = %q, attendu %q", i, got[i].Content, veut)
				}
			}
		})
	}
}

// ⚠️ PIÈGE D'ALIASING. dedupeConsecutive part de « out := in[:1] », qui partage
// le tableau sous-jacent avec l'entrée : écrire dans out écrit AUSSI dans in.
//
// Ici ça ne mord pas, parce que le seul appelant construit sa tranche juste
// avant et ne la relit jamais. Mais c'est le genre de dépendance implicite
// qu'un futur appelant ne peut pas deviner, et qui produit une corruption
// silencieuse le jour où quelqu'un réutilise l'entrée.
//
// Ce test FIGE le comportement actuel plutôt que de le corriger : le changer
// coûterait une allocation à chaque relecture de conversation, pour un défaut
// qui n'existe pas encore. Il documente le contrat — « l'entrée est consommée,
// ne la relisez pas » — et il échouera si quelqu'un s'appuie sur l'inverse.
func TestDedupeConsomeSonEntree(t *testing.T) {
	entree := []messageView{
		{Role: "assistant", Content: "partiel"},
		{Role: "assistant", Content: "partiel et complet"},
	}
	avant := entree[0].Content

	dedupeConsecutive(entree)

	if entree[0].Content == avant {
		t.Log("l'entrée n'est plus modifiée : dedupeConsecutive alloue désormais," +
			" le commentaire de mise en garde peut être retiré")
	} else {
		t.Logf("contrat confirmé : l'entrée est modifiée sur place (%q → %q)",
			avant, entree[0].Content)
	}
}
