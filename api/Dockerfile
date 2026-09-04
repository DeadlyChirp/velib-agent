# Build multi-étapes : on compile dans une image Go complète, on livre un
# binaire statique dans une image minimale.
#
# Le conteneur final ne contient ni compilateur, ni shell, ni gestionnaire de
# paquets : moins de 20 Mo, et une surface d'attaque réduite au strict binaire.

FROM golang:1.26-alpine AS build
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
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-w -s" \
    -o /out/api ./cmd/api

# ── Image finale ─────────────────────────────────────────────────────────────
FROM alpine:3.22
# Les certificats racine sont indispensables : sans eux, l'appel HTTPS vers
# l'API Vélib' et vers le fournisseur de modèle échoue sur une erreur de
# vérification de certificat, message peu explicite pour qui débute.
RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 appuser

COPY --from=build /out/api /usr/local/bin/api

# On ne tourne pas en root : si le processus est compromis, il n'a pas les
# droits pour aller plus loin.
USER appuser
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/api"]
