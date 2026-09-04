# Build multi-étapes : on compile dans une image Go complète, on livre un
# binaire statique dans une image minimale.
#
# Le conteneur final ne contient ni compilateur ni gestionnaire de paquets :
# une trentaine de mégaoctets, et une surface d'attaque réduite au binaire.

# ⚠️ golang:1.27-alpine et NON 1.26. L'image golang:1.26-alpine publiée sur
# Docker Hub (digest ce864e72…) embarque un binaire Go qui refuse de s'exécuter
# — « exec format error » — alors que l'architecture correspond bien, x86_64 des
# deux côtés, vérifié.
#
# Le mode d'échec est le pire possible : le shell renvoie 0, aucun binaire n'est
# produit, et le build ne casse qu'à l'étape COPY suivante avec un message qui
# désigne le mauvais coupable. Vérifié : 1.24, 1.25 et 1.27 fonctionnent. On prend 1.27 car go.mod exige >= 1.26.5.
# Version épinglée plutôt qu'« alpine » flottant, pour un build reproductible.
FROM golang:1.27-alpine AS build
WORKDIR /src

# Les dépendances d'abord, dans leur propre couche : tant que go.mod et go.sum
# ne bougent pas, Docker réutilise le cache et ne retélécharge rien.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO désactivé pour obtenir un binaire réellement statique, seule façon de le
# faire tourner dans une image sans libc.
# Les drapeaux -w -s retirent la table des symboles et le DWARF : binaire plus
# léger, sans conséquence sur les traces de panique.
#
# mkdir explicite puis test -x : « go build -o /out/api » peut sortir en code 0
# sans rien écrire quand /out n'existe pas. On rend cette panne bruyante ici
# plutôt que de la découvrir trois étapes plus loin.
RUN mkdir -p /out \
 && CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /out/api ./cmd/api \
 && test -x /out/api

# ── Image finale ─────────────────────────────────────────────────────────────
FROM alpine:3.22

# Les certificats racine sont indispensables : sans eux, l'appel HTTPS vers
# l'API Vélib' et vers le fournisseur de modèle échoue sur une erreur de
# vérification de certificat, message peu explicite pour qui débute.
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -u 10001 appuser

COPY --from=build /out/api /usr/local/bin/api

# On ne tourne pas en root : si le processus est compromis, il n'a pas les
# droits pour aller plus loin.
USER appuser
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/api"]
