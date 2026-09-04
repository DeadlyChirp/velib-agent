// Package observability compte ce qui se passe pendant l'exécution.
//
// Pourquoi ce paquet existe : un agent qui répond vite et juste est
// indistinguable, de l'extérieur, d'un agent qui répond vite et faux en servant
// une donnée de trois heures. Sans mesure, on découvre la panne par un
// utilisateur mécontent, pas par un tableau de bord.
//
// Ce n'est PAS un remplaçant de Langfuse ou d'OpenTelemetry. C'est le minimum
// qu'un service doit exposer sur lui-même pour être diagnosticable sans
// dépendance externe, et le point de branchement naturel le jour où on ajoute
// un vrai exportateur OTLP : les mêmes événements y sont déjà collectés.
package observability

import (
	"sort"
	"sync"
	"time"
)

// maxRecentTurns borne l'historique conservé en mémoire.
//
// Une mémoire qui grandit avec le trafic est une fuite déguisée : au bout de
// deux jours, le processus meurt sur un OOM et personne ne comprend pourquoi.
// Cinquante tours suffisent à diagnostiquer un comportement anormal.
const maxRecentTurns = 50

// Metrics agrège les compteurs du service.
//
// Tout est protégé par un seul mutex. Un verrou par famille de compteurs serait
// plus fin et beaucoup plus facile à casser : ici les sections critiques se
// comptent en microsecondes et la contention n'est pas le problème.
type Metrics struct {
	mu sync.RWMutex

	startedAt time.Time

	tools map[string]*toolStat
	cache cacheStat
	turns turnStat

	recent []TurnRecord
}

type toolStat struct {
	Calls     int64
	Errors    int64
	TotalMs   int64
	MaxMs     int64
	durations []int64 // pour les centiles ; borné, voir record()
}

type cacheStat struct {
	Hits          int64
	Misses        int64
	Refreshes     int64
	RefreshErrors int64
	StaleServed   int64
	LastFetch     time.Time
	LastStations  int
}

type turnStat struct {
	Total            int64
	Errors           int64
	Empty            int64
	TotalMs          int64
	PromptTokens     int64
	CompletionTokens int64
}

// TurnRecord est la trace d'un tour de conversation.
//
// Aucun contenu de message n'est conservé : le tableau de bord est accessible
// sans authentification, et y exposer les questions des utilisateurs
// transformerait un outil de diagnostic en fuite de données.
type TurnRecord struct {
	At               time.Time `json:"at"`
	ConversationID   string    `json:"conversation_id"`
	DurationMs       int64     `json:"duration_ms"`
	Tools            []string  `json:"tools"`
	AnswerChars      int       `json:"answer_chars"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	Status           string    `json:"status"` // ok | error | empty
	DataStale        bool      `json:"data_stale"`
}

// New construit un collecteur.
func New() *Metrics {
	return &Metrics{
		startedAt: time.Now(),
		tools:     make(map[string]*toolStat),
		recent:    make([]TurnRecord, 0, maxRecentTurns),
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Enregistrement
// ─────────────────────────────────────────────────────────────────────────────

// RecordTool note un appel d'outil.
func (m *Metrics) RecordTool(name string, d time.Duration, failed bool) {
	ms := d.Milliseconds()

	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.tools[name]
	if !ok {
		s = &toolStat{}
		m.tools[name] = s
	}
	s.Calls++
	s.TotalMs += ms
	if ms > s.MaxMs {
		s.MaxMs = ms
	}
	if failed {
		s.Errors++
	}
	// Fenêtre glissante pour les centiles : on garde les 200 derniers appels
	// plutôt que tout l'historique, même raison que pour recent.
	s.durations = append(s.durations, ms)
	if len(s.durations) > 200 {
		s.durations = s.durations[len(s.durations)-200:]
	}
}

// RecordCacheHit note une lecture servie sans appel réseau.
func (m *Metrics) RecordCacheHit() {
	m.mu.Lock()
	m.cache.Hits++
	m.mu.Unlock()
}

// RecordCacheRefresh note un rafraîchissement, réussi ou non.
func (m *Metrics) RecordCacheRefresh(stations int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cache.Misses++
	m.cache.Refreshes++
	if err != nil {
		m.cache.RefreshErrors++
		return
	}
	m.cache.LastFetch = time.Now()
	m.cache.LastStations = stations
}

// RecordStaleServed note qu'une donnée périmée a été servie faute de mieux.
//
// C'est le compteur le plus important du tableau de bord : il est à zéro en
// fonctionnement normal, et son passage à un non-zéro est le signal que la
// source amont a un problème — avant qu'un utilisateur ne s'en plaigne.
func (m *Metrics) RecordStaleServed() {
	m.mu.Lock()
	m.cache.StaleServed++
	m.mu.Unlock()
}

// RecordTurn note un tour de conversation complet.
func (m *Metrics) RecordTurn(r TurnRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.turns.Total++
	m.turns.TotalMs += r.DurationMs
	m.turns.PromptTokens += int64(r.PromptTokens)
	m.turns.CompletionTokens += int64(r.CompletionTokens)
	switch r.Status {
	case "error":
		m.turns.Errors++
	case "empty":
		m.turns.Empty++
	}

	m.recent = append(m.recent, r)
	if len(m.recent) > maxRecentTurns {
		m.recent = m.recent[len(m.recent)-maxRecentTurns:]
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Lecture
// ─────────────────────────────────────────────────────────────────────────────

// ToolView est la vue d'un outil.
type ToolView struct {
	Name      string  `json:"name"`
	Calls     int64   `json:"calls"`
	Errors    int64   `json:"errors"`
	ErrorRate float64 `json:"error_rate_pct"`
	AvgMs     int64   `json:"avg_ms"`
	P50Ms     int64   `json:"p50_ms"`
	P95Ms     int64   `json:"p95_ms"`
	MaxMs     int64   `json:"max_ms"`
}

// CacheView est la vue du cache.
type CacheView struct {
	Hits          int64   `json:"hits"`
	Misses        int64   `json:"misses"`
	HitRatePct    float64 `json:"hit_rate_pct"`
	Refreshes     int64   `json:"refreshes"`
	RefreshErrors int64   `json:"refresh_errors"`
	StaleServed   int64   `json:"stale_served"`
	LastFetchAgeS int64   `json:"last_fetch_age_s"`
	Stations      int     `json:"stations"`
}

// TurnView est la vue des conversations.
type TurnView struct {
	Total            int64   `json:"total"`
	Errors           int64   `json:"errors"`
	Empty            int64   `json:"empty"`
	AvgMs            int64   `json:"avg_ms"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	AvgTokensPerTurn int64   `json:"avg_tokens_per_turn"`
	ErrorRatePct     float64 `json:"error_rate_pct"`
}

// Snapshot est l'état complet, sérialisable.
type Snapshot struct {
	UptimeSeconds int64        `json:"uptime_seconds"`
	Tools         []ToolView   `json:"tools"`
	Cache         CacheView    `json:"cache"`
	Turns         TurnView     `json:"turns"`
	Recent        []TurnRecord `json:"recent_turns"`
}

// Snapshot rend l'état courant.
func (m *Metrics) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := Snapshot{
		UptimeSeconds: int64(time.Since(m.startedAt).Seconds()),
		Tools:         make([]ToolView, 0, len(m.tools)),
	}

	for name, s := range m.tools {
		v := ToolView{
			Name:   name,
			Calls:  s.Calls,
			Errors: s.Errors,
			MaxMs:  s.MaxMs,
		}
		if s.Calls > 0 {
			v.AvgMs = s.TotalMs / s.Calls
			v.ErrorRate = pct(s.Errors, s.Calls)
		}
		v.P50Ms = percentile(s.durations, 50)
		v.P95Ms = percentile(s.durations, 95)
		out.Tools = append(out.Tools, v)
	}
	// Ordre stable : sans tri, l'ordre de parcours d'une map change à chaque
	// appel et le tableau de bord danse sous les yeux de celui qui le lit.
	sort.Slice(out.Tools, func(i, j int) bool {
		if out.Tools[i].Calls != out.Tools[j].Calls {
			return out.Tools[i].Calls > out.Tools[j].Calls
		}
		return out.Tools[i].Name < out.Tools[j].Name
	})

	out.Cache = CacheView{
		Hits:          m.cache.Hits,
		Misses:        m.cache.Misses,
		HitRatePct:    pct(m.cache.Hits, m.cache.Hits+m.cache.Misses),
		Refreshes:     m.cache.Refreshes,
		RefreshErrors: m.cache.RefreshErrors,
		StaleServed:   m.cache.StaleServed,
		Stations:      m.cache.LastStations,
	}
	if !m.cache.LastFetch.IsZero() {
		out.Cache.LastFetchAgeS = int64(time.Since(m.cache.LastFetch).Seconds())
	} else {
		out.Cache.LastFetchAgeS = -1 // jamais chargé, à distinguer de « chargé il y a 0 s »
	}

	out.Turns = TurnView{
		Total:            m.turns.Total,
		Errors:           m.turns.Errors,
		Empty:            m.turns.Empty,
		PromptTokens:     m.turns.PromptTokens,
		CompletionTokens: m.turns.CompletionTokens,
		TotalTokens:      m.turns.PromptTokens + m.turns.CompletionTokens,
		ErrorRatePct:     pct(m.turns.Errors, m.turns.Total),
	}
	if m.turns.Total > 0 {
		out.Turns.AvgMs = m.turns.TotalMs / m.turns.Total
		out.Turns.AvgTokensPerTurn = out.Turns.TotalTokens / m.turns.Total
	}

	// Copie défensive : rendre la tranche interne laisserait l'appelant lire
	// pendant qu'une écriture la remplace.
	out.Recent = make([]TurnRecord, len(m.recent))
	copy(out.Recent, m.recent)
	// Le plus récent en tête, c'est ce qu'on regarde en premier.
	for i, j := 0, len(out.Recent)-1; i < j; i, j = i+1, j-1 {
		out.Recent[i], out.Recent[j] = out.Recent[j], out.Recent[i]
	}

	return out
}

// percentile rend le centile demandé, en millisecondes.
//
// Interpolation volontairement absente : sur 200 échantillons de latence, la
// précision au dixième de milliseconde n'apporte rien et ajoute du code à
// justifier.
func percentile(v []int64, p int) int64 {
	if len(v) == 0 {
		return 0
	}
	s := make([]int64, len(v))
	copy(s, v)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })

	idx := (p * len(s)) / 100
	if idx >= len(s) {
		idx = len(s) - 1
	}
	return s[idx]
}

func pct(part, total int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(int64(float64(part)/float64(total)*10000+0.5)) / 100
}
