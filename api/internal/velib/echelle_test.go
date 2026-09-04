package velib

import (
	"fmt"
	"runtime"
	"testing"
	"time"
)

// Bancs à l'échelle : le parc parisien fait 1 519 stations, mais rien ne garantit
// que ça reste vrai — la métropole s'étend, et le même service appliqué à un
// opérateur national verrait cent fois plus.
//
// On mesure donc à 1×, 10×, 100× et 1000× pour voir CE QUI casse en premier,
// plutôt que d'optimiser au jugé.
//
//	go test ./internal/velib -bench Echelle -benchmem -run XXX
var echelles = []int{1_519, 15_190, 151_900, 1_519_000}

func parcDe(n int) Snapshot {
	st := make([]Station, 0, n)
	for i := 0; i < n; i++ {
		s := Station{
			ID:             int64(i),
			Name:           fmt.Sprintf("Station %d - Quartier %d", i, i%400),
			Capacity:       30,
			BikesAvailable: i % 30,
			DocksAvailable: i % 25,
			IsInstalled:    true,
			IsRenting:      i%50 != 0,
			IsReturning:    true,
		}
		s.searchKey = normalize(s.Name)
		st = append(st, s)
	}
	return Snapshot{Stations: st, FetchedAt: time.Now()}
}

func BenchmarkEchelleSearch(b *testing.B) {
	for _, n := range echelles {
		snap := parcDe(n)
		b.Run(fmt.Sprintf("%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = Search(snap.Stations, "Quartier 42", 5)
			}
		})
	}
}

func BenchmarkEchelleRank(b *testing.B) {
	for _, n := range echelles {
		snap := parcDe(n)
		b.Run(fmt.Sprintf("%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = Rank(snap, "bikes_available", 5, false, time.Now())
			}
		})
	}
}

func BenchmarkEchelleCount(b *testing.B) {
	for _, n := range echelles {
		snap := parcDe(n)
		b.Run(fmt.Sprintf("%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = Count(snap, "empty", 5, time.Now())
			}
		})
	}
}

func BenchmarkEchelleSummarize(b *testing.B) {
	for _, n := range echelles {
		snap := parcDe(n)
		b.Run(fmt.Sprintf("%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = Summarize(snap, time.Now())
			}
		})
	}
}

// Le coût qu'aucun banc de latence ne montre : ce que le parc occupe en
// mémoire, puisqu'il est gardé en entier dans le cache.
func TestEmpreinteMemoire(t *testing.T) {
	if testing.Short() {
		t.Skip("mesure mémoire, ignorée en mode court")
	}
	for _, n := range []int{1_519, 151_900, 1_519_000} {
		runtime.GC()
		var avant runtime.MemStats
		runtime.ReadMemStats(&avant)

		snap := parcDe(n)

		runtime.GC()
		var apres runtime.MemStats
		runtime.ReadMemStats(&apres)

		mo := float64(apres.HeapAlloc-avant.HeapAlloc) / (1 << 20)
		t.Logf("%9d stations : %7.1f Mo en mémoire (%.0f octets/station)",
			n, mo, mo*(1<<20)/float64(n))
		runtime.KeepAlive(snap)
	}
}

func BenchmarkEchelleFindStations(b *testing.B) {
	for _, n := range echelles {
		snap := parcDe(n)
		b.Run(fmt.Sprintf("%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = FindStations(snap, "Quartier 42", time.Now())
			}
		})
	}
}
