package velib

// topK rend les k meilleurs éléments de idx, triés, sans trier le reste.
//
// POURQUOI cette fonction existe. Les classements ne rendent jamais plus de
// vingt stations, mais l'implémentation précédente triait tout le parc pour en
// garder cinq. À l'échelle du parc parisien — 1 519 stations — le tri complet
// coûtait 575 µs et la question ne se posait pas. Mesuré à mille fois ce
// volume, il passait à 1 096 ms : le seul endroit du projet dont le coût
// dépassait O(n).
//
// COMMENT. On garde une liste triée des k meilleurs candidats vus jusqu'ici.
// Chaque élément est d'abord comparé au PIRE des retenus : s'il ne le bat pas,
// on passe au suivant sans rien déplacer. Une fois la liste remplie, c'est le
// cas de la quasi-totalité des éléments, et le coût par élément se réduit à une
// comparaison.
//
// Le pire des cas — une entrée déjà triée à l'envers, où chaque élément bat le
// précédent — reste O(n·k), avec k plafonné à vingt. Un tas donnerait O(n·log k)
// et demanderait cinq méthodes d'interface pour gagner sur un k qui ne dépasse
// jamais vingt : le compte n'y est pas.
//
// meilleur(a, b) doit dire si l'élément d'indice a se classe AVANT celui
// d'indice b, et former un ordre total — départager les ex aequo, sans quoi
// deux exécutions sur la même donnée peuvent rendre deux classements différents.
func topK(idx []int, k int, meilleur func(a, b int) bool) []int {
	if k <= 0 || len(idx) == 0 {
		return nil
	}
	if k >= len(idx) {
		k = len(idx)
	}

	best := make([]int, 0, k)
	for _, cand := range idx {
		// La liste est pleine et le candidat ne bat pas le dernier : rien à
		// faire. C'est le chemin emprunté par la quasi-totalité des éléments.
		if len(best) == k && !meilleur(cand, best[k-1]) {
			continue
		}

		// Recherche dichotomique de la position d'insertion. Linéaire ferait
		// l'affaire pour k ≤ 20, mais la version dichotomique n'est pas plus
		// longue à lire et ne se dégrade pas si le plafond monte un jour.
		lo, hi := 0, len(best)
		for lo < hi {
			mid := (lo + hi) / 2
			if meilleur(cand, best[mid]) {
				hi = mid
			} else {
				lo = mid + 1
			}
		}

		if len(best) < k {
			best = append(best, 0)
		}
		copy(best[lo+1:], best[lo:len(best)-1])
		best[lo] = cand
	}
	return best
}
