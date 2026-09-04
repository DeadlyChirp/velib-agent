package velib

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// DefaultInformationURL est le référentiel : nom, position, capacité.
	// Quasi statique, il ne bouge qu'à l'ouverture ou la fermeture d'une station.
	DefaultInformationURL = "https://velib-metropole-opendata.smovengo.cloud/opendata/Velib_Metropole/station_information.json"

	// DefaultStatusURL est l'état temps réel : vélos et bornes disponibles.
	DefaultStatusURL = "https://velib-metropole-opendata.smovengo.cloud/opendata/Velib_Metropole/station_status.json"
)

// Client télécharge et décode les deux flux Vélib'.
//
// Il ne fait QUE ça. Il ne cache rien et ne décide rien : la politique de
// fraîcheur vit dans le cache, ce qui permet de tester le téléchargement et la
// politique séparément.
type Client struct {
	informationURL string
	statusURL      string
	http           *http.Client
	maxAttempts    int
}

// ClientOption configure le client.
type ClientOption func(*Client)

func WithInformationURL(u string) ClientOption   { return func(c *Client) { c.informationURL = u } }
func WithStatusURL(u string) ClientOption        { return func(c *Client) { c.statusURL = u } }
func WithHTTPClient(h *http.Client) ClientOption { return func(c *Client) { c.http = h } }
func WithMaxAttempts(n int) ClientOption         { return func(c *Client) { c.maxAttempts = n } }

// NewClient construit un client avec des réglages prudents par défaut.
func NewClient(opts ...ClientOption) *Client {
	c := &Client{
		informationURL: DefaultInformationURL,
		statusURL:      DefaultStatusURL,
		maxAttempts:    3,
		http: &http.Client{
			// 15 s couvre largement 464 Ko sur une liaison correcte. Un appel
			// sans plafond de temps est exactement le mécanisme qui a produit
			// une perte de données silencieuse de onze jours sur un projet
			// précédent : rien ne levait d'erreur, rien ne loguait, tout
			// attendait. Toute sortie réseau porte désormais une limite.
			Timeout: 15 * time.Second,
		},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Fetch télécharge les deux flux et rend le parc joint.
//
// Les deux téléchargements partent EN PARALLÈLE : ils sont indépendants, donc
// l'attente est celle du plus lent et non leur somme.
func (c *Client) Fetch(ctx context.Context) (Snapshot, error) {
	type infoResult struct {
		feed rawInformationFeed
		err  error
	}
	type statusResult struct {
		feed rawStatusFeed
		err  error
	}

	infoCh := make(chan infoResult, 1)
	statusCh := make(chan statusResult, 1)

	go func() {
		var f rawInformationFeed
		err := c.getJSON(ctx, c.informationURL, &f)
		infoCh <- infoResult{f, err}
	}()
	go func() {
		var f rawStatusFeed
		err := c.getJSON(ctx, c.statusURL, &f)
		statusCh <- statusResult{f, err}
	}()

	info := <-infoCh
	status := <-statusCh

	if info.err != nil {
		return Snapshot{}, fmt.Errorf("référentiel des stations : %w", info.err)
	}
	if status.err != nil {
		return Snapshot{}, fmt.Errorf("état des stations : %w", status.err)
	}

	// Une réponse HTTP 200 avec zéro station est un échec silencieux, pas un
	// succès : on refuse de construire un parc vide qui ferait répondre
	// « il y a 0 vélo » avec assurance.
	if len(info.feed.Data.Stations) == 0 {
		return Snapshot{}, fmt.Errorf("référentiel vide : réponse valide mais sans station")
	}
	if len(status.feed.Data.Stations) == 0 {
		return Snapshot{}, fmt.Errorf("état vide : réponse valide mais sans station")
	}

	return Snapshot{
		Stations:  join(info.feed.Data.Stations, status.feed.Data.Stations),
		FetchedAt: time.Now(),
	}, nil
}

// getJSON exécute la requête avec quelques tentatives et un repli exponentiel.
//
// On retente uniquement ce qui a une chance d'aboutir : erreurs réseau, 429 et
// 5xx. Un 404 ne se répare pas en insistant, et une annulation du contexte doit
// remonter immédiatement.
func (c *Client) getJSON(ctx context.Context, url string, dst any) error {
	var lastErr error
	backoff := 300 * time.Millisecond

	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
				backoff *= 2
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err // requête malformée : inutile de retenter
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "velib-agent/1.0")

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("statut %d", resp.StatusCode)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return fmt.Errorf("statut %d, non rejouable", resp.StatusCode)
		}

		// Plafond de lecture : une source qui se met à répondre un flux infini
		// ne doit pas pouvoir faire gonfler la mémoire du processus. 16 Mo
		// laissent vingt fois la marge nécessaire aux 464 Ko attendus.
		body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if err := json.Unmarshal(body, dst); err != nil {
			// Un JSON illisible ne se répare pas en retentant.
			return fmt.Errorf("décodage : %w", err)
		}
		return nil
	}
	return fmt.Errorf("après %d tentatives : %w", c.maxAttempts, lastErr)
}
