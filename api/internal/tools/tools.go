// Package tools expose le parc Vélib' à l'agent.
//
// C'est la frontière du système : tout ce qui sort d'ici entre dans la fenêtre
// de contexte du modèle. Deux règles gouvernent le paquet.
//
// 1. AUCUN OUTIL NE RENVOIE LE PARC. Il n'existe pas de get_all_stations, et
// c'est délibéré. Les données pèsent 748 Ko pour 1519 stations et la source ne
// filtre pas côté serveur : la réduction doit donc se faire ici, en Go.
//
// 2. TOUTE TAILLE DE SORTIE EST BORNÉE CÔTÉ SERVEUR. Le modèle peut demander
// « les 500 premières » : c'est notre code qui refuse, pas sa bonne volonté. Un
// outil dont la taille de réponse dépend d'un paramètre choisi par le modèle
// finira par saturer le contexte le jour où la question sera posée autrement.
package tools

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/tool"
	"trpc.group/trpc-go/trpc-agent-go/tool/function"

	"velib-agent/internal/velib"
)

// Registry construit les outils au-dessus d'une source de parc.
type Registry struct {
	source velib.Source
	log    *slog.Logger
	now    func() time.Time // injectable pour les tests
}

// NewRegistry construit le registre.
func NewRegistry(src velib.Source, log *slog.Logger) *Registry {
	if log == nil {
		log = slog.Default()
	}
	return &Registry{source: src, log: log, now: time.Now}
}

// All rend les outils dans l'ordre où ils seront présentés au modèle.
func (r *Registry) All() []tool.Tool {
	return []tool.Tool{
		r.networkSummary(),
		r.findStation(),
		r.rankStations(),
		r.countStations(),
	}
}

// snapshot récupère le parc et journalise l'appel.
//
// Une erreur n'est jamais renvoyée telle quelle au modèle : un message d'erreur
// brut le pousse à combler le vide en inventant. On renvoie une structure qui
// lui dit quoi faire — et le cache, lui, a déjà tenté de servir une donnée
// datée avant d'en arriver là.
func (r *Registry) snapshot(ctx context.Context, toolName string) (velib.Snapshot, *ToolError) {
	start := time.Now()
	snap, err := r.source.Snapshot(ctx)
	elapsed := time.Since(start)

	if err != nil {
		r.log.Error("outil en échec", "outil", toolName, "erreur", err, "duree", elapsed)
		return velib.Snapshot{}, &ToolError{
			Error: "les données Vélib' sont momentanément indisponibles",
			Advice: "informer l'utilisateur que la source est injoignable et " +
				"proposer de réessayer, ne pas inventer de chiffres",
		}
	}

	r.log.Info("outil appelé", "outil", toolName,
		"duree_ms", elapsed.Milliseconds(),
		"stations", len(snap.Stations),
		"donnee_peremee", snap.Stale)
	return snap, nil
}

// ToolError est la forme d'échec exposée au modèle : jamais une erreur Go
// brute, toujours un message plus une consigne.
type ToolError struct {
	Error  string `json:"error"`
	Advice string `json:"advice"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Outil 1 — vue d'ensemble du parc
// ─────────────────────────────────────────────────────────────────────────────

type summaryInput struct {
	// Aucun paramètre. Le champ existe uniquement parce que le générateur de
	// schéma a besoin d'une struct ; il est ignoré.
	Unused string `json:"unused,omitempty" jsonschema:"description=ne pas renseigner"`
}

type summaryOutput struct {
	*velib.NetworkSummary
	*ToolError
}

func (r *Registry) networkSummary() tool.Tool {
	fn := func(ctx context.Context, _ summaryInput) (summaryOutput, error) {
		snap, terr := r.snapshot(ctx, "network_summary")
		if terr != nil {
			return summaryOutput{ToolError: terr}, nil
		}
		s := velib.Summarize(snap, r.now())
		return summaryOutput{NetworkSummary: &s}, nil
	}

	return function.NewFunctionTool(fn,
		function.WithName("network_summary"),
		function.WithDescription(
			"Donne les totaux de TOUT le réseau Vélib' en un seul appel : nombre "+
				"de stations, stations hors service et leur pourcentage, stations vides "+
				"et pleines, vélos disponibles répartis entre mécaniques et électriques, "+
				"bornes libres et capacité totale. "+
				"À utiliser dès qu'une question porte sur l'ensemble du parc plutôt que "+
				"sur des stations précises, par exemple « combien de vélos électriques "+
				"en tout » ou « quel pourcentage de stations est hors service ». "+
				"Le champ out_of_service_rule donne la définition exacte appliquée pour "+
				"« hors service » : la citer si l'utilisateur peut en douter. "+
				"Ne prend aucun paramètre."),
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// Outil 2 — recherche d'une station par son nom
// ─────────────────────────────────────────────────────────────────────────────

type findInput struct {
	Name string `json:"name" jsonschema:"description=Nom ou fragment de nom de la station cherchée. La recherche ignore la casse et les accents. Un fragment suffit: « Benjamin Godard » trouve « Benjamin Godard - Victor Hugo »,required"`
}

type findOutput struct {
	*velib.StationDetail
	*ToolError
}

func (r *Registry) findStation() tool.Tool {
	fn := func(ctx context.Context, in findInput) (findOutput, error) {
		snap, terr := r.snapshot(ctx, "find_station")
		if terr != nil {
			return findOutput{ToolError: terr}, nil
		}
		d := velib.FindStations(snap, in.Name, r.now())
		return findOutput{StationDetail: &d}, nil
	}

	return function.NewFunctionTool(fn,
		function.WithName("find_station"),
		function.WithDescription(
			"Cherche une ou plusieurs stations par leur nom et renvoie leur état : "+
				"vélos disponibles, dont électriques, bornes libres, capacité. "+
				"La recherche est tolérante : elle ignore la casse et les accents, et "+
				"accepte un fragment de nom. "+
				"ATTENTION, elle renvoie jusqu'à trois candidats car plusieurs stations "+
				"portent des noms proches ou identiques — « gare de lyon » correspond à "+
				"trois stations distinctes. Si le champ ambiguous vaut true, DEMANDER à "+
				"l'utilisateur laquelle il vise au lieu d'en choisir une. "+
				"Si match_count vaut 0, la station n'existe pas dans le parc : le dire "+
				"et proposer une reformulation, ne jamais inventer de chiffres."),
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// Outil 3 — classement des stations
// ─────────────────────────────────────────────────────────────────────────────

type rankInput struct {
	Metric    string `json:"metric" jsonschema:"description=Critère de classement,enum=docks_available,enum=bikes_available,enum=ebikes,enum=capacity,required"`
	Limit     int    `json:"limit" jsonschema:"description=Nombre de stations à renvoyer. Défaut 5, maximum 20 imposé par le serveur"`
	Ascending bool   `json:"ascending" jsonschema:"description=true pour un classement croissant (les plus petites valeurs), false ou absent pour décroissant (les plus grandes)"`
}

type rankOutput struct {
	*velib.RankResult
	*ToolError
}

func (r *Registry) rankStations() tool.Tool {
	fn := func(ctx context.Context, in rankInput) (rankOutput, error) {
		snap, terr := r.snapshot(ctx, "rank_stations")
		if terr != nil {
			return rankOutput{ToolError: terr}, nil
		}
		res := velib.Rank(snap, velib.Metric(in.Metric), in.Limit, in.Ascending, r.now())
		return rankOutput{RankResult: &res}, nil
	}

	return function.NewFunctionTool(fn,
		function.WithName("rank_stations"),
		function.WithDescription(
			"Classe les stations selon un critère et renvoie seulement le sommet du "+
				"classement. "+
				"À utiliser pour les questions de type « les cinq stations qui ont le "+
				"plus de bornes libres » ou « quelles stations ont le plus de vélos "+
				"électriques ». "+
				"Critères disponibles : docks_available (bornes libres pour rendre un "+
				"vélo), bikes_available (vélos à prendre), ebikes (vélos électriques), "+
				"capacity (taille de la station). "+
				"Le serveur borne la réponse à 20 stations maximum quelle que soit la "+
				"valeur demandée. Ne pas utiliser cet outil pour obtenir la liste "+
				"complète du parc : elle n'est pas disponible, et c'est volontaire."),
	)
}

// ─────────────────────────────────────────────────────────────────────────────
// Outil 4 — comptage avec échantillon
// ─────────────────────────────────────────────────────────────────────────────

type countInput struct {
	Filter     string `json:"filter" jsonschema:"description=Critère de sélection des stations,enum=empty,enum=full,enum=out_of_service,enum=has_ebikes,required"`
	SampleSize int    `json:"sample_size" jsonschema:"description=Nombre d'exemples à joindre au total. Défaut 5, maximum 10 imposé par le serveur"`
}

type countOutput struct {
	*velib.CountResult
	*ToolError
}

func (r *Registry) countStations() tool.Tool {
	fn := func(ctx context.Context, in countInput) (countOutput, error) {
		snap, terr := r.snapshot(ctx, "count_stations")
		if terr != nil {
			return countOutput{ToolError: terr}, nil
		}
		size := in.SampleSize
		if size == 0 {
			size = 5
		}
		res := velib.Count(snap, velib.Filter(in.Filter), size, r.now())
		return countOutput{CountResult: &res}, nil
	}

	return function.NewFunctionTool(fn,
		function.WithName("count_stations"),
		function.WithDescription(
			"Compte les stations correspondant à un critère et en renvoie un "+
				"ÉCHANTILLON, pas la liste complète. "+
				"C'est l'outil à utiliser pour « liste-moi toutes les stations vides », "+
				"« combien de stations sont pleines », « quelles stations sont hors "+
				"service ». "+
				"Critères : empty (aucun vélo à prendre), full (aucune borne libre), "+
				"out_of_service (station démontée, qui ne prête plus ou ne reprend plus), "+
				"has_ebikes (au moins un vélo électrique). "+
				"Le champ total est EXACT et doit être annoncé tel quel. Le champ sample "+
				"ne contient que quelques exemples : quand truncated vaut true, dire "+
				"clairement à l'utilisateur combien il y en a au total et que la liste "+
				"affichée est partielle, puis proposer d'affiner. Le nombre de stations "+
				"concernées varie fortement dans la journée, une liste exhaustive n'aurait "+
				"aucune valeur pratique."),
	)
}

// SystemInstruction est l'instruction donnée à l'agent.
//
// Elle est courte et impérative. Chaque phrase répond à un échec observé ou
// anticipé, plutôt que de décrire un personnage : un long prompt de rôle
// consomme du contexte à chaque tour sans améliorer le comportement.
const SystemInstruction = `Tu es un assistant qui répond à des questions sur le parc de stations Vélib' de Paris et sa métropole.

Règles de travail :
- Tu ne connais RIEN du parc par toi-même. Toute donnée chiffrée doit venir d'un appel d'outil, jamais de ta mémoire.
- Pour une question sur l'ensemble du réseau, un seul appel à network_summary suffit. N'enchaîne pas plusieurs outils quand un seul répond.
- Quand find_station renvoie ambiguous=true, demande à l'utilisateur laquelle des stations il vise. Ne choisis pas à sa place.
- Quand count_stations renvoie truncated=true, annonce le total exact et précise que tu ne montres que quelques exemples.
- La liste complète des stations n'est pas accessible, par conception. Si on te la demande, explique-le et propose un comptage ou un classement.
- Quand une réponse porte un bloc freshness avec stale=true, ou qu'une station porte data_age_seconds, dis-le. Une donnée datée annoncée comme telle est utile ; une donnée datée présentée comme fraîche est un mensonge.
- Si un outil renvoie un champ error, explique la situation à l'utilisateur et n'invente aucun chiffre.

Style : réponds en français, brièvement, avec les chiffres exacts. Pas de préambule. Quand tu cites un pourcentage de stations hors service, mentionne la définition donnée par out_of_service_rule si elle peut prêter à discussion.`

// Describe rend un résumé lisible des outils, utile en journal de démarrage.
func Describe(ts []tool.Tool) string {
	out := ""
	for _, t := range ts {
		d := t.Declaration()
		out += fmt.Sprintf("  - %s\n", d.Name)
	}
	return out
}
