package velib

import (
	"math/rand"
	"slices"
	"sort"
	"testing"
)

// Le test qui compte : topK doit rendre EXACTEMENT ce que rendrait un tri
// complet suivi d'une troncature. Remplacer un tri par une sélection est une
// optimisation ; si elle change une seule réponse, ce n'est plus une
// optimisation, c'est un bug.
//
// On le vérifie sur des données aléatoires, avec beaucoup d'ex aequo — c'est là
// que les implémentations naïves divergent.
func TestTopKEquivautAuTriComplet(t *testing.T) {
	r := rand.New(rand.NewSource(1))

	for essai := 0; essai < 300; essai++ {
		n := r.Intn(200)
		k := r.Intn(25)

		// Peu de valeurs distinctes : force les ex aequo.
		valeurs := make([]int, n)
		for i := range valeurs {
			valeurs[i] = r.Intn(5)
		}

		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		// Ordre total : valeur décroissante, puis indice croissant.
		meilleur := func(a, b int) bool {
			if valeurs[a] != valeurs[b] {
				return valeurs[a] > valeurs[b]
			}
			return a < b
		}

		attendu := slices.Clone(idx)
		sort.SliceStable(attendu, func(a, b int) bool {
			return meilleur(attendu[a], attendu[b])
		})
		limite := k
		if limite > len(attendu) {
			limite = len(attendu)
		}
		attendu = attendu[:limite]

		got := topK(idx, k, meilleur)

		if !slices.Equal(got, attendu) {
			t.Fatalf("essai %d (n=%d, k=%d)\n  topK = %v\n  tri  = %v\n  valeurs = %v",
				essai, n, k, got, attendu, valeurs)
		}
	}
}

func TestTopKCasLimites(t *testing.T) {
	croissant := func(vals []int) func(a, b int) bool {
		return func(a, b int) bool { return vals[a] < vals[b] }
	}

	t.Run("k nul", func(t *testing.T) {
		if got := topK([]int{0, 1, 2}, 0, croissant([]int{3, 1, 2})); len(got) != 0 {
			t.Errorf("k=0 devrait rendre vide, obtenu %v", got)
		}
	})
	t.Run("k négatif", func(t *testing.T) {
		if got := topK([]int{0, 1}, -5, croissant([]int{1, 2})); len(got) != 0 {
			t.Errorf("k négatif devrait rendre vide, obtenu %v", got)
		}
	})
	t.Run("entrée vide", func(t *testing.T) {
		if got := topK(nil, 5, croissant(nil)); len(got) != 0 {
			t.Errorf("entrée vide devrait rendre vide, obtenu %v", got)
		}
	})
	t.Run("k plus grand que l'entrée", func(t *testing.T) {
		vals := []int{3, 1, 2}
		got := topK([]int{0, 1, 2}, 100, croissant(vals))
		if !slices.Equal(got, []int{1, 2, 0}) { // valeurs 1, 2, 3
			t.Errorf("obtenu %v, attendu [1 2 0]", got)
		}
	})
	t.Run("un seul élément", func(t *testing.T) {
		vals := []int{9}
		got := topK([]int{0}, 5, croissant(vals))
		if !slices.Equal(got, []int{0}) {
			t.Errorf("obtenu %v, attendu [0]", got)
		}
	})
}

// Le pire des cas de l'insertion : une entrée déjà classée à l'envers, où
// CHAQUE élément bat tous les retenus et provoque un décalage complet. Le
// résultat doit rester juste — c'est le seul point non négociable, le coût
// restant borné par k.
func TestTopKPireCasOrdreInverse(t *testing.T) {
	const n = 1000
	vals := make([]int, n)
	idx := make([]int, n)
	for i := range vals {
		vals[i] = i // croissant, donc chaque nouveau bat les précédents
		idx[i] = i
	}
	got := topK(idx, 5, func(a, b int) bool { return vals[a] > vals[b] })

	attendu := []int{999, 998, 997, 996, 995}
	if !slices.Equal(got, attendu) {
		t.Errorf("obtenu %v, attendu %v", got, attendu)
	}
}
