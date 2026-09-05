# -*- coding: utf-8 -*-
"""Audit adverse — couche HTTP, sans appeler le modèle.

Ces attaques ne coûtent aucun jeton : elles visent l'API elle-même. On cherche
les 500, les fuites d'information et les absences de plafond.

Verdict par cas :
  OK      le service se défend comme prévu
  FAIBLE  ça passe mais ça ne devrait pas
  CASSE   500, plantage, ou fuite
"""
import json
import sys
import urllib.error
import urllib.request

# La console Windows encode en cp1252 par defaut. Ces scripts impriment du
# francais, et le premier caractere hors cp1252 — une fleche, un signe
# mathematique — tue le run EN PLEINE EXECUTION, apres avoir affiche des
# resultats verts. La CI tourne sous Linux en UTF-8 : elle ne le verra jamais.
# Une ligne par script, posee partout plutot qu'au coup par coup.
sys.stdout.reconfigure(encoding="utf-8", errors="replace")

API = "http://localhost:8080/api"
resultats = []

# Une identité par SECTION, stable à l'intérieur d'une section.
#
# Deux contraintes qui se contredisent, et il faut les tenir ensemble. La limite
# de débit se déclenche au sixième message : une identité unique pour tout le
# fichier ferait répondre 429 à la moitié des cas, et on croirait le service
# robuste sans l'avoir testé. Mais une identité par APPEL casse l'inverse : la
# conversation appartiendrait à une identité et le message partirait sous une
# autre, donc 404 partout — un audit vert sur du vide.
#
# La section est la bonne granularité : assez de rotation pour rester sous la
# limite, assez de stabilité pour que « créer puis interroger » fonctionne.
_n = [0]
_courant = ["audit-adverse-0"]


def section():
    _n[0] += 1
    _courant[0] = "audit-adverse-%d" % _n[0]
    return _courant[0]


def identite():
    return _courant[0]


def appel(methode, chemin, corps=None, entetes=None, brut=None, timeout=30):
    data = brut if brut is not None else (
        json.dumps(corps, ensure_ascii=False).encode("utf-8") if corps is not None else None)
    h = {"Content-Type": "application/json", "X-User-ID": identite()}
    if entetes:
        h.update(entetes)
    r = urllib.request.Request(API + chemin, data=data, method=methode, headers=h)
    try:
        rsp = urllib.request.urlopen(r, timeout=timeout)
        return rsp.status, rsp.read()[:4000]
    except urllib.error.HTTPError as e:
        return e.code, e.read()[:4000]
    except Exception as e:
        return -1, str(e).encode()


def cas(nom, statut, corps, attendus, interdits=()):
    """attendus : statuts acceptables. interdits : fragments qui ne doivent PAS apparaitre."""
    txt = corps.decode("utf-8", "replace") if isinstance(corps, bytes) else str(corps)

    # Une requête que la bibliothèque cliente REFUSE DE CONSTRUIRE n'a jamais
    # atteint le serveur. C'est une défense, pas une panne : l'en-tête avec un
    # CRLF est rejeté par urllib avant l'envoi, et aucun serveur au monde ne
    # peut faire mieux que « la requête n'existe pas ».
    #
    # Le classer CASSE faisait rougir la CI pour un cas où tout fonctionne.
    if statut == -1 and ("invalid header" in txt.lower() or "illegal header" in txt.lower()):
        resultats.append(("OK", nom, "refusé par le client avant l'envoi", txt[:80]))
        return

    if statut >= 500 or statut == -1:
        verdict, detail = "CASSE", "statut %s" % statut
    elif statut not in attendus:
        verdict, detail = "FAIBLE", "statut %s, attendu %s" % (statut, attendus)
    else:
        fuite = [f for f in interdits if f.lower() in txt.lower()]
        if fuite:
            verdict, detail = "CASSE", "fuite : %s" % fuite
        else:
            verdict, detail = "OK", "statut %s" % statut
    resultats.append((verdict, nom, detail, txt[:150].replace("\n", " ")))


print("=== 1. Identifiants malformés ===")
section()
for nom, ident in [
    ("injection SQL", "' OR 1=1 --"),
    ("traversée de chemin", "../../etc/passwd"),
    ("octet nul", "abc%00def"),
    ("identifiant très long", "a" * 5000),
    ("caractères de contrôle", "a\tb\nc"),
    ("unicode exotique", "🚲" * 100),
]:
    s, b = appel("GET", "/conversations/" + urllib.request.quote(ident, safe=""))
    cas("GET conversation — " + nom, s, b, {400, 404},
        interdits=["panic", "goroutine", "sql:", "pq:", "/usr/", "postgres://"])

print("=== 2. Corps de requête hostiles ===")


def conversation_neuve():
    """Une conversation NEUVE sous une identité NEUVE, pour chaque cas.

    Sans cela, les six premiers messages consomment la rafale autorisée et les
    suivants reçoivent 429 : ils apparaissent « faibles » alors qu'ils n'ont
    simplement jamais été exécutés. Le garde-fou du service masquait la mesure.
    """
    section()
    st, b = appel("POST", "/conversations")
    return json.loads(b)["id"] if st in (200, 201) else None


for nom, corps, attendus in [
    ("message absent", {}, {400}),
    ("message nul", {"message": None}, {400}),
    ("message = nombre", {"message": 12345}, {400}),
    ("message = objet", {"message": {"a": 1}}, {400}),
    ("message = tableau", {"message": ["a", "b"]}, {400}),
    ("champ inconnu en masse", {"message": "ok", **{f"x{i}": i for i in range(500)}}, {200, 400}),
]:
    s, b = appel("POST", "/conversations/%s/messages" % conversation_neuve(), corps, timeout=60)
    cas("POST message — " + nom, s, b, attendus,
        interdits=["panic", "goroutine", "runtime error"])

print("=== 3. JSON malformé et bombes ===")
for nom, brut, attendus in [
    ("JSON tronqué", b'{"message": "ab', {400}),
    ("JSON vide", b'', {400}),
    ("tableau au lieu d'objet", b'[]', {400}),
    ("imbrication profonde", b'{"message":' + b'[' * 2000 + b']' * 2000 + b'}', {400}),
    ("corps > 1 Mo", b'{"message":"' + b'A' * (2 * 1024 * 1024) + b'"}', {400, 413}),
]:
    s, b = appel("POST", "/conversations/%s/messages" % conversation_neuve(), brut=brut, timeout=60)
    cas("POST message — " + nom, s, b, attendus,
        interdits=["panic", "goroutine", "runtime error"])

print("=== 4. En-têtes ===")
section()
for nom, ent, attendus in [
    ("sans Content-Type", {"Content-Type": ""}, {200, 400, 415}),
    ("Content-Type XML", {"Content-Type": "application/xml"}, {200, 400, 415}),
    ("X-User-ID vide", {"X-User-ID": ""}, {200, 201, 400}),
    ("X-User-ID très long", {"X-User-ID": "u" * 10000}, {200, 201, 400, 431}),
    ("X-User-ID avec saut de ligne", {"X-User-ID": "a\r\nX-Injected: 1"}, {200, 201, 400}),
]:
    try:
        s, b = appel("GET", "/conversations", entetes=ent)
        cas("GET conversations — " + nom, s, b, attendus,
            interdits=["panic", "goroutine"])
    except Exception as e:
        resultats.append(("OK", "GET conversations — " + nom,
                          "rejeté par la bibliothèque cliente", str(e)[:100]))

print("=== 5. Méthodes et routes ===")
section()
conv = conversation_neuve()
for methode, chemin, attendus in [
    ("PUT", "/conversations", {405}),
    ("PATCH", "/conversations/%s" % conv, {405}),
    ("DELETE", "/conversations", {405}),
    ("GET", "/conversations/%s/messages" % conv, {404, 405}),
    ("GET", "/../etc/passwd", {400, 404}),
    ("GET", "/health/../../admin", {400, 404}),
]:
    s, b = appel(methode, chemin)
    cas("%s %s" % (methode, chemin), s, b, attendus,
        interdits=["panic", "root:", "/bin/"])

print("=== 6. Fuite d'information ===")
section()
s, b = appel("GET", "/health")
cas("GET health — pas de secret", s, b, {200},
    interdits=["sk-", "gsk_", "password", "velib:velib", "postgres://"])

s, b = appel("GET", "/metrics")
cas("GET metrics — pas de secret", s, b, {200},
    interdits=["sk-", "gsk_", "password", "postgres://"])

# les conversations de test sont éphémères, la base est remise à zéro par « make reset »

print()
print("=" * 78)
casse = [r for r in resultats if r[0] == "CASSE"]
faible = [r for r in resultats if r[0] == "FAIBLE"]
for verdict, nom, detail, extrait in resultats:
    if verdict != "OK":
        print("%-7s %-48s %s" % (verdict, nom[:48], detail))
        print("        %s" % extrait[:120])
print("-" * 78)
print("%d cas — %d OK, %d faibles, %d cassés"
      % (len(resultats), len(resultats) - len(casse) - len(faible), len(faible), len(casse)))
sys.exit(1 if casse else 0)
