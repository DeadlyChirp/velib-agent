# Agent conversationnel — parc Vélib' Paris

Un agent qui répond en langage naturel sur les 1 519 stations Vélib' de Paris et
sa métropole. API Go avec `trpc-agent-go`, conversations persistées en
PostgreSQL, réponse en streaming, front statique. `docker compose up` et c'est
en ligne.

---

## Lancer

```bash
cp .env.example .env      # puis renseigner OPENAI_API_KEY
docker compose up --build
```

Le front est sur **http://localhost:3000**, l'API sur **http://localhost:8080**.

Le modèle se choisit par variable d'environnement. Tout fournisseur compatible
OpenAI convient — OpenAI, Mistral, DeepSeek, ou un Ollama local — en changeant
`MODEL_NAME` et `OPENAI_BASE_URL`. Les combinaisons testées sont dans
`.env.example`.

```bash
make test        # 18 tests unitaires, sans réseau ni base
make test-live   # tests contre la vraie API Vélib'
make reset       # arrête la pile ET efface les conversations
```

---

## Les décisions, et pourquoi

### 1. Aucun outil ne renvoie la liste des stations

C'est la seule décision qui compte vraiment, et tout le reste en découle.

Le parc pèse **748 618 octets** pour 1 519 stations, et la source ne filtre pas
côté serveur : c'est tout ou rien. Un outil qui renverrait le parc reproduirait
exactement le problème que la spécification demande de résoudre.

Les quatre outils agrègent donc **en Go** et ne rendent que de petits résultats
bornés. Mesuré sur les données réelles :

```
parc brut sérialisé : 446 157 octets
  network_summary       454 octets   (réduction  982x)
  rank_stations         915 octets   (réduction  487x)
  count_stations      1 591 octets   (réduction  280x)
  find_station          240 octets   (réduction 1858x)
```

Un test (`TestToolOutputsStaySmall`) échoue si une sortie dépasse cette borne :
l'invariant est protégé, il ne se dégradera pas discrètement.

### 2. « Toutes les stations vides » renvoie un compte, pas une liste

La cinquième question de référence demande une liste non bornée. J'ai mesuré deux
fois le même jour, à une heure d'intervalle :

| | 10 h | 11 h |
|---|---|---|
| Stations vides | 49 (3,2 %) | **115 (7,6 %)** |

Rien dans le code n'avait changé — seulement la météo et l'heure. Un dimanche
soir de pluie, ce serait plusieurs centaines.

`count_stations` renvoie donc `{total, sample, truncated}` : le **total est
exact**, l'échantillon est borné à dix côté serveur. L'agent répond « il y en a
115, en voici dix ». Ça répond mieux à l'intention réelle — savoir *combien* —
et la taille de sortie ne dépend plus de la météo.

### 3. « Hors service » : j'ai choisi une définition, et je la publie

La spécification demande le pourcentage de stations hors service sans définir le terme.
Trois drapeaux existent et donnent trois réponses :

| Définition | Stations | Part |
|---|---|---|
| `is_installed = 0` seul | 1 | 0,07 % |
| `is_renting = 0` | 16 | 1,05 % |
| au moins un drapeau à 0 | 16 | 1,05 % |

J'ai retenu la définition **orientée usager** : une station est hors service si
elle est démontée, ne prête plus ou ne reprend plus de vélo. Elle est renvoyée
au modèle avec le chiffre, dans le champ `out_of_service_rule`, pour qu'il
puisse la citer plutôt que d'énoncer un pourcentage sans contexte.

Un chiffre sans sa définition est indéfendable devant un client.

### 4. La recherche renvoie des candidats, jamais « la » station

Mesuré sur le parc réel : **532 stations sur 1 519 portent des accents**, trois
noms sont en **double**, et « gare de lyon » correspond à **trois stations
distinctes**. « Benjamin Godard », la question de référence, ne correspond à aucun
nom exact — le nom réel est « Benjamin Godard - Victor Hugo ».

La recherche normalise donc la casse et les accents, et renvoie jusqu'à trois
candidats avec un indicateur `ambiguous`. Choisir arbitrairement, c'est répondre
faux avec assurance.

### 5. Le cache sert la donnée périmée plutôt qu'une erreur

Sans cache, chaque appel d'outil retéléchargerait 464 Ko. Le référentiel est
quasi statique, l'état bouge à la minute : TTL de 60 secondes, et un
singleflight maison pour qu'une expiration ne déclenche qu'**un seul**
rafraîchissement même sous dix requêtes simultanées.

Le comportement en panne est délibéré : si la source est injoignable, on sert la
dernière donnée connue **en la marquant périmée**, avec son âge. Un agent qui
reçoit une erreur brute comble le vide en inventant ; un agent qui reçoit « voici
la donnée, elle a quarante minutes, la source est injoignable » peut le dire
honnêtement.

### 6. Les sessions PostgreSQL viennent du framework

J'avais commencé à écrire ma propre table de messages. En lisant le code de
`trpc-agent-go` avant de continuer, j'ai trouvé `session/postgres`, dont
l'interface expose exactement les quatre opérations demandées : `CreateSession`,
`ListSessions`, `GetSession`, `DeleteSession`.

J'ai supprimé ma couche. Moins de code à maintenir, et un comportement de reprise
de session fidèle à ce que le Runner attend plutôt qu'une réimplémentation
approximative.

### 7. Les données sales, traitées explicitement

Trois anomalies mesurées sur le parc réel, dont deux font planter du code naïf :

- **4 stations ont `capacity = 0`.** `OccupancyRate()` renvoie
  `(float64, bool)` et refuse de produire un chiffre plutôt que `+Inf`.
- **15 stations ont `bikes + docks > capacity`**, physiquement impossible. Les
  bornes libres sont donc **lues** dans `num_docks_available`, jamais calculées
  par soustraction.
- **`num_bikes_available_types` est un tableau d'objets à une clé**
  (`[{"mechanical":3},{"ebike":4}]`), pas un objet. Un décodage en
  `map[string]int` compile, ne lève rien, et renvoie zéro vélo électrique sur
  tout le parc. Décodé en `[]map[string]int`, fusionné **par clé** et non par
  position.

### 8. La fraîcheur est exposée au modèle

La documentation des API annonce un rafraîchissement « toutes les minutes ». C'est vrai du
**fichier**, pas des stations : mesuré, **441 stations muettes depuis plus d'une
heure**, la plus ancienne à 48 499 heures — une valeur sentinelle.

Chaque réponse d'outil porte un bloc `freshness`, et une station dont la
remontée dépasse une heure porte son âge. Les autres non : ajouter un champ
« tout va bien » à chaque ligne gaspillerait du contexte pour rien.

### 9. SSE plutôt que WebSocket

Le flux est unidirectionnel. SSE est du HTTP simple, se reconnecte tout seul
côté navigateur et traverse les proxys sans négociation. Un WebSocket aurait
ajouté une machine à états bidirectionnelle pour un besoin qui ne l'est pas.

Deux détails qui cassent le streaming en silence, traités : `proxy_buffering
off` côté nginx, et une méthode `Flush()` sur l'enveloppe de journalisation —
sans elle, l'assertion `http.Flusher` échoue et la réponse arrive d'un bloc à la
fin, sans qu'aucune erreur n'apparaisse nulle part.

### 10. Un front d'un seul fichier

Pas de React, pas de `node_modules`, pas d'étape de build. L'écran compte une
liste et une zone de conversation ; l'image finale est un nginx qui sert un
fichier statique. Ce qui est fait sérieusement quand même : streaming token par
token, affichage des appels d'outils en cours, gestion des erreurs, navigation
au clavier, et `textContent` partout — jamais `innerHTML`, puisque le contenu
vient d'un modèle.

---

## Ce que j'ai écarté, et pourquoi c'était un choix

- **Le géocodage d'adresses et la météo** (extensions optionnelles). Aucune des cinq
  questions n'en a besoin. La spécification dit que trois choses qui tiennent debout
  valent mieux que dix à moitié branchées : j'ai préféré durcir ce qui existe.
- **Le filtrage par arrondissement.** Les données Vélib' ne portent pas
  d'arrondissement, et un cinquième des stations est hors Paris. Le faire
  proprement demandait un appel de géocodage par station.
- **Une authentification.** Non demandée. L'en-tête `X-User-ID` isole les
  conversations, et le modèle de données du framework est déjà
  multi-utilisateurs : brancher un vrai jeton ne demanderait que de remplir cet
  en-tête depuis une identité vérifiée. **Tel quel, un utilisateur qui change
  l'en-tête voit les conversations d'un autre** — acceptable pour un test, à
  fermer avant toute mise en ligne.
- **La dépendance `golang.org/x/sync/singleflight`.** Vingt lignes avec un mutex
  et un channel font le travail pour une seule clé, et se défendent en revue.
- **Une distance de Levenshtein** pour la recherche. Plus fine sur le papier,
  elle fait remonter des noms qui ne partagent aucun mot avec la requête. Trois
  paliers explicites sont moins impressionnants et beaucoup plus faciles à
  expliquer quand un résultat surprend.

---

## Avec plus de temps

1. **Historiser les snapshots** pour répondre aux questions de tendance
   (« cette station est-elle souvent vide le matin ? »). C'est la seule
   fonctionnalité qui changerait la nature du produit plutôt que de l'élargir.
2. **Tests de bout en bout** sur le flux SSE et le cycle de vie des
   conversations. Les agrégations sont couvertes, la couche HTTP ne l'est pas.
3. **Traces OpenTelemetry.** Le framework les expose, et Langfuse est un backend
   OTLP : le coût et la latence par outil deviendraient visibles sans code
   supplémentaire.
4. **Fermer l'usurpation d'identité** de `X-User-ID` derrière une vraie
   authentification.

---

## Architecture

```
web/          front statique servi par nginx, proxy /api vers l'API
api/
  cmd/api/            démarrage, arrêt propre, mode sonde
  internal/config/    lecture et validation de l'environnement
  internal/velib/     client HTTP, cache, jointure, AGRÉGATIONS (fonctions pures)
  internal/tools/     les 4 outils exposés au modèle + instruction système
  internal/agent/     câblage Runner, modèle, sessions PostgreSQL
  internal/httpapi/   routeur, CRUD conversations, streaming SSE
```

La frontière qui compte est celle de `internal/velib` : les agrégations sont des
fonctions pures qui prennent un `Snapshot` et rendent une petite valeur. Elles se
testent **sans réseau, sans base et sans LLM**, ce qui a permis de prouver que
les cinq questions de référence avaient la bonne réponse avant même de brancher un
modèle.

Le journal de bord — mesures, pièges, et ce qui a été généré puis corrigé — est
dans [`NOTES.md`](NOTES.md).
