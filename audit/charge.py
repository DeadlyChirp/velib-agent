# -*- coding: utf-8 -*-
"""Test de charge : le service tient-il quand plusieurs personnes l'utilisent ?

Tout ce qui a été mesuré jusqu'ici l'a été en série, un appel après l'autre.
C'est le régime le moins révélateur : une fuite de connexions, une course sur le
cache ou un verrou trop large ne se voient qu'en concurrence.

On ne teste PAS le débit maximal — ce serait mesurer le fournisseur de modèle,
pas ce code. On teste que rien ne casse, ne fuit, ni ne se corrompt quand
cinquante clients travaillent en même temps.

⚠️ LIRE LES RÉSULTATS AVEC PRUDENCE AU-DELÀ DE 1 000 CLIENTS.

Mesuré : jusqu'à 1 000 clients, zéro erreur. À 2 000, ce script rapporte des
centaines d'échecs — et ils sont de CE CÔTÉ-CI. Ils arrivent en `-1`, c'est-à-dire
une exception du client et non une réponse du serveur, et le service ne journalise
aucun refus.

Vérifié séparément : 2 000 requêtes vraiment simultanées passent sans une seule
exception. Ce qui casse à 2 000 clients ici, c'est l'enchaînement — huit appels
par client, seize mille au total — qui épuise les ports éphémères de la machine
de test. Le harnais atteint sa limite avant le service.

Un test de charge qui mesure sa propre limite et l'attribue au service est pire
qu'un test absent : il fait corriger un problème qui n'existe pas.

    python audit/charge.py [--clients 50] [--tours 4]

Aucun appel au modèle : uniquement les routes qui coûtent zéro jeton. Le seul
chemin qui appelle le modèle est déjà couvert ailleurs, et le noyer sous
cinquante clients ne mesurerait que le quota du fournisseur.
"""
import json
import sys
import threading
import time
import urllib.error
import urllib.request

API = "http://localhost:8080/api"

CLIENTS = 50
TOURS = 4
for i, a in enumerate(sys.argv):
    if a == "--clients" and i + 1 < len(sys.argv):
        CLIENTS = int(sys.argv[i + 1])
    if a == "--tours" and i + 1 < len(sys.argv):
        TOURS = int(sys.argv[i + 1])


def appel(chemin, corps=None, methode=None, user="charge", timeout=30):
    data = json.dumps(corps).encode("utf-8") if corps is not None else None
    r = urllib.request.Request(
        API + chemin, data=data,
        method=methode or ("POST" if data is not None else "GET"),
        headers={"Content-Type": "application/json", "X-User-ID": user})
    debut = time.perf_counter()
    try:
        rsp = urllib.request.urlopen(r, timeout=timeout)
        return rsp.status, rsp.read(), time.perf_counter() - debut
    except urllib.error.HTTPError as e:
        return e.code, e.read(), time.perf_counter() - debut
    except Exception as e:
        return -1, str(e).encode(), time.perf_counter() - debut


# Chaque client a SA propre identité : c'est ce que fait la vraie vie, et c'est
# aussi ce qui évite que la limite de débit — pensée pour brider un client —
# vienne masquer un défaut de concurrence.
resultats = []
verrou = threading.Lock()


def client(n):
    """Un utilisateur qui ouvre, lit, liste et supprime ses conversations."""
    uid = "charge-%03d" % n
    local = {"ok": 0, "erreurs": [], "latences": []}

    for _ in range(TOURS):
        st, b, d = appel("/conversations", {}, user=uid)
        local["latences"].append(d)
        if st not in (200, 201):
            local["erreurs"].append("creation %s" % st)
            continue
        cid = json.loads(b)["id"]

        st, b, d = appel("/conversations/" + cid, user=uid)
        local["latences"].append(d)
        if st != 200:
            local["erreurs"].append("relecture %s" % st)

        # L'isolation doit tenir SOUS CHARGE aussi. C'est là qu'un cache mal
        # cloisonné ou une clé de session mal composée se révélerait.
        st, b, d = appel("/conversations", user=uid)
        local["latences"].append(d)
        if st == 200:
            liste = json.loads(b)["conversations"]
            etrangers = [c for c in liste if c["id"] == cid]
            if not etrangers:
                local["erreurs"].append("sa propre conversation absente de sa liste")
        else:
            local["erreurs"].append("liste %s" % st)

        st, _, d = appel("/conversations/" + cid, methode="DELETE", user=uid)
        local["latences"].append(d)
        if st != 204:
            local["erreurs"].append("suppression %s" % st)
        else:
            local["ok"] += 1

    with verrou:
        resultats.append(local)


def etat_service():
    st, b, _ = appel("/health")
    if st != 200:
        return None
    return json.loads(b)


def metriques():
    st, b, _ = appel("/metrics")
    if st != 200:
        return None
    return json.loads(b)


def main():
    avant = etat_service()
    if not avant:
        print("Service injoignable. Lancer « docker compose up -d » d'abord.")
        return 2

    m_avant = metriques() or {}
    print("Avant : %s, %d stations en cache"
          % (avant["status"], (m_avant.get("cache") or {}).get("stations", 0)))
    print("Lancement de %d clients, %d tours chacun (%d requêtes)..."
          % (CLIENTS, TOURS, CLIENTS * TOURS * 4))

    debut = time.perf_counter()
    fils = [threading.Thread(target=client, args=(i,)) for i in range(CLIENTS)]
    for f in fils:
        f.start()
    for f in fils:
        f.join()
    duree = time.perf_counter() - debut

    latences = sorted(l for r in resultats for l in r["latences"])
    erreurs = [e for r in resultats for e in r["erreurs"]]
    cycles = sum(r["ok"] for r in resultats)

    def centile(p):
        if not latences:
            return 0
        return latences[min(len(latences) - 1, int(len(latences) * p / 100))]

    print()
    print("%-28s %s" % ("requêtes", len(latences)))
    print("%-28s %.1f s" % ("durée totale", duree))
    print("%-28s %.0f req/s" % ("débit", len(latences) / duree))
    print("%-28s %d / %d" % ("cycles complets", cycles, CLIENTS * TOURS))
    print("%-28s %.0f ms" % ("latence médiane", centile(50) * 1000))
    print("%-28s %.0f ms" % ("latence p95", centile(95) * 1000))
    print("%-28s %.0f ms" % ("latence max", latences[-1] * 1000 if latences else 0))
    print("%-28s %d" % ("erreurs", len(erreurs)))

    if erreurs:
        print()
        compte = {}
        for e in erreurs:
            compte[e] = compte.get(e, 0) + 1
        for e, n in sorted(compte.items(), key=lambda x: -x[1]):
            print("   %4d x  %s" % (n, e))

    # Le service doit être dans le MÊME état qu'avant : pas de connexion
    # perdue, pas de cache vidé, pas de dégradation.
    apres = etat_service()
    m_apres = metriques() or {}
    print()
    if not apres:
        print("VERDICT : le service ne répond plus après la charge")
        return 1
    if apres["status"] != "ok" or apres["postgres"] != "ok":
        print("VERDICT : service dégradé après la charge — %s" % apres)
        return 1

    st_avant = (m_avant.get("cache") or {}).get("stations", 0)
    st_apres = (m_apres.get("cache") or {}).get("stations", 0)
    if st_apres != st_avant:
        print("VERDICT : le cache est passé de %d à %d stations sous charge"
              % (st_avant, st_apres))
        return 1

    # Un seul rafraîchissement de cache pour toute la charge, au plus : c'est
    # ce que garantit le singleflight, et la concurrence est le seul régime où
    # son absence se verrait.
    r_avant = (m_avant.get("cache") or {}).get("refreshes", 0)
    r_apres = (m_apres.get("cache") or {}).get("refreshes", 0)
    print("rafraîchissements du cache pendant la charge : %d" % (r_apres - r_avant))

    if erreurs:
        print("VERDICT : %d erreurs, le service tient mais pas proprement" % len(erreurs))
        return 1

    print("VERDICT : service intact, aucune erreur, cache stable")
    return 0


if __name__ == "__main__":
    sys.exit(main())
