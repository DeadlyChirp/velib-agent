package velib

import (
	"fmt"
	"testing"
	"time"
)

func benchStations(n int) []Station {
	out := make([]Station, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, Station{
			ID: int64(i), Name: fmt.Sprintf("Station %d - Quartier %d", i, i%20),
			Capacity: 30, BikesAvailable: i % 30, DocksAvailable: i % 25,
			IsInstalled: true, IsRenting: true, IsReturning: true,
		})
	}
	return out
}

// Chemin de PRODUCTION : searchKey rempli par join().
func BenchmarkSearchPrecalcule(b *testing.B) {
	st := benchStations(1519)
	for i := range st {
		st[i].searchKey = normalize(st[i].Name)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = searchTop(st, "Quartier 7", 5)
	}
}

// Chemin de REPLI : searchKey vide, normalisation a chaque comparaison.
func BenchmarkSearchRepli(b *testing.B) {
	st := benchStations(1519)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = searchTop(st, "Quartier 7", 5)
	}
}

func BenchmarkNormalize(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = normalize("Benjamin Godard - Victor Hugo")
	}
}

func BenchmarkSummarize(b *testing.B) {
	snap := Snapshot{Stations: benchStations(1519)}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Summarize(snap, time.Now())
	}
}
