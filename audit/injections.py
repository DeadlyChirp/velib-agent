# -*- coding: utf-8 -*-
"""Injections, toutes familles — sans appeler le modèle.

attaques_api.py couvre les entrées malformées. Ce fichier va chercher les
familles d'injection classiques, une par une, y compris celles qui ne
s'appliquent probablement pas : vérifier qu'un vecteur ne s'applique PAS a une
valeur, parce que c'est ce qui permet de le dire en soutenance sans bluffer.

    python audit/injections.py

Verdicts :
  OK           le service se défend, ou le vecteur ne s'applique pas
  À REGARDER   le comportement mérite un œil
  CASSE        500, fuite, ou exécution
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

_n = [0]
_courant = ["inj-0"]


def section():
    _n[0] += 1
    _courant[0] = "inj-%d" % _n[0]


def appel(methode, chemin, corps=None, brut=None, entetes=None, timeout=30):
    data = brut if brut is not None else (
        json.dumps(corps, ensure_ascii=False).encode("utf-8") if corps is not None else None)
    h = {"Content-Type": "application/json", "X-User-ID": _courant[0]}
    if entetes:
        h.update(entetes)
    r = urllib.request.Request(API + chemin, data=data, method=methode, headers=h)
    try:
        rsp = urllib.request.urlopen(r, timeout=timeout)
        return rsp.status, rsp.read()[:3000]
    except urllib.error.HTTPError as e:
        return e.code, e.read()[:3000]
    except Exception as e:
        return -1, ("%s: %s" % (type(e).__name__, e)).encode()


resultats = []


def juge(famille, nom, statut, corps, interdits=()):
    txt = corps.decode("utf-8", "replace") if isinstance(corps, bytes) else str(corps)

    # Une requête que le client refuse de construire n'atteint jamais le
    # serveur : c'est une défense, pas une panne.
    if statut == -1 and "header" in txt.lower():
        verdict, detail = "OK", "refusé par le client avant l'envoi"
    elif statut >= 500:
        verdict, detail = "CASSE", "statut %d" % statut
    elif statut == -1:
        verdict, detail = "À REGARDER", txt[:60]
    else:
        fuites = [f for f in interdits if f.lower() in txt.lower()]
        verdict = "CASSE" if fuites else "OK"
        detail = ("fuite : %s" % fuites) if fuites else ("statut %d" % statut)

    resultats.append((verdict, famille, nom, detail))
    print("%-11s %-14s %-40s %s" % (verdict, famille, nom[:40], detail[:44]))


# Fragments qui ne doivent JAMAIS apparaître dans une réponse : trace
# d'exécution Go, erreur SQL brute, chaîne de connexion, contenu système.
FUITES = ["panic:", "goroutine ", "runtime error", "pq:", "sql:", "SQLSTATE",
          "postgres://", "root:x:", "/etc/passwd", "sk-", "gsk_",
          "BEGIN RSA", "Authorization:"]


def creer():
    st, b = appel("POST", "/conversations", {})
    return json.loads(b)["id"] if st in (200, 201) else None


print("%-11s %-14s %-40s %s" % ("VERDICT", "FAMILLE", "VECTEUR", "DÉTAIL"))
print("-" * 112)

# ── SQL ─────────────────────────────────────────────────────────────────────
#
# Le framework utilise des requêtes préparées, donc ces vecteurs ne devraient
# rien donner. On le vérifie plutôt que de le supposer : « on utilise un ORM »
# n'est pas une preuve, c'est une intention.
section()
for nom, charge in [
    ("guillemet simple", "'"),
    ("OR 1=1", "' OR '1'='1"),
    ("UNION SELECT", "' UNION SELECT NULL,NULL--"),
    ("commentaire", "admin'--"),
    ("DROP TABLE", "'; DROP TABLE sessions;--"),
    ("temporisation pg_sleep", "'; SELECT pg_sleep(5);--"),
    ("empilement", "1; DELETE FROM sessions"),
]:
    st, b = appel("GET", "/conversations/" + urllib.request.quote(charge, safe=""))
    juge("SQL", nom, st, b, FUITES)

# Le même vecteur par l'en-tête d'identité, qui sert de clé de session.
section()
for nom, charge in [("via X-User-ID", "' OR 1=1--"), ("via X-User-ID nul", "a\x00b")]:
    st, b = appel("GET", "/conversations", entetes={"X-User-ID": charge})
    juge("SQL", nom, st, b, FUITES)

# ── Chemin et fichiers ──────────────────────────────────────────────────────
section()
for nom, charge in [
    ("../ simple", "../../../etc/passwd"),
    ("encodé", "..%2f..%2f..%2fetc%2fpasswd"),
    ("double encodé", "..%252f..%252fetc%252fpasswd"),
    ("backslash", "..\\..\\windows\\win.ini"),
    ("absolu", "/etc/passwd"),
    ("UNC", "\\\\serveur\\partage"),
]:
    st, b = appel("GET", "/conversations/" + urllib.request.quote(charge, safe=""))
    juge("Chemin", nom, st, b, FUITES)

# ── Commande ────────────────────────────────────────────────────────────────
#
# Aucun exec dans ce service : ces vecteurs ne devraient pas s'appliquer. On
# vérifie qu'ils ne produisent ni 500 ni sortie de commande.
section()
for nom, charge in [
    ("point-virgule", "; id"),
    ("substitution", "$(whoami)"),
    ("antiquotes", "`id`"),
    ("pipe", "| cat /etc/passwd"),
    ("et logique", "&& ls -la"),
    ("saut de ligne", "test\nid"),
]:
    st, b = appel("POST", "/conversations/%s/messages" % (creer() or "x"),
                  {"message": "Combien de stations %s" % charge}, timeout=20)
    juge("Commande", nom, st, b, FUITES + ["uid=", "gid=", "/bin/"])

# ── JSON et désérialisation ─────────────────────────────────────────────────
section()
for nom, brut in [
    ("prototype pollution", b'{"message":"ok","__proto__":{"admin":true}}'),
    ("constructor", b'{"message":"ok","constructor":{"prototype":{"x":1}}}'),
    ("cle dupliquee", b'{"message":"innocent","message":"remplace"}'),
    ("profondeur 5000", b'{"message":' + b'[' * 5000 + b']' * 5000 + b'}'),
    ("nombre demesure", b'{"message":"ok","n":1e400}'),
    ("unicode echappe", b'{"message":"\\u0000\\u001f"}'),
    ("BOM en tete", b'\xef\xbb\xbf{"message":"ok"}'),
]:
    st, b = appel("POST", "/conversations/%s/messages" % (creer() or "x"),
                  brut=brut, timeout=20)
    juge("JSON", nom, st, b, FUITES)

# ── En-têtes HTTP ───────────────────────────────────────────────────────────
section()
for nom, ent in [
    ("Host falsifie", {"Host": "evil.example.com"}),
    ("X-Forwarded-Host", {"X-Forwarded-Host": "evil.example.com"}),
    ("X-Original-URL", {"X-Original-URL": "/admin"}),
    ("Content-Type errone", {"Content-Type": "text/plain"}),
    ("Transfer-Encoding", {"Transfer-Encoding": "chunked"}),
    ("Accept-Encoding exotique", {"Accept-Encoding": "gzip;q=1.0, *;q=0"}),
]:
    st, b = appel("GET", "/conversations", entetes=ent)
    juge("En-tête", nom, st, b, FUITES)

# ── Unicode ─────────────────────────────────────────────────────────────────
#
# Les filtres naïfs se contournent par la normalisation : une forme composée
# peut passer là où la forme simple est refusée.
section()
for nom, charge in [
    ("surrogates", "𐀀"),
    ("normalisation NFKC", "ﬁ" * 100),
    ("bidi override", "‮txet‬"),
    ("zero-width", "a​b‌c‍d"),
    ("combinants empiles", "e" + "́" * 200),
    ("pleine chasse", "ｓｅｌｅｃｔ"),
]:
    st, b = appel("POST", "/conversations/%s/messages" % (creer() or "x"),
                  {"message": charge}, timeout=20)
    juge("Unicode", nom, st, b, FUITES)

# ── SSRF ────────────────────────────────────────────────────────────────────
#
# Le service ne prend aucune URL en entrée : le vecteur ne s'applique pas. On le
# vérifie pour pouvoir le dire, plutôt que de l'affirmer.
section()
for nom, charge in [
    ("métadonnées cloud", "http://169.254.169.254/latest/meta-data/"),
    ("boucle locale", "http://127.0.0.1:5432/"),
    ("fichier local", "file:///etc/passwd"),
    ("gopher", "gopher://127.0.0.1:5432/_"),
]:
    st, b = appel("POST", "/conversations/%s/messages" % (creer() or "x"),
                  {"message": "Combien de stations à %s" % charge}, timeout=20)
    juge("SSRF", nom, st, b, FUITES + ["ami-", "instance-id", "root:"])

# ── Verdict ─────────────────────────────────────────────────────────────────
print("-" * 112)
casse = [r for r in resultats if r[0] == "CASSE"]
regarder = [r for r in resultats if r[0] == "À REGARDER"]
print("%d vecteurs — %d OK, %d à regarder, %d cassés"
      % (len(resultats), len(resultats) - len(casse) - len(regarder),
         len(regarder), len(casse)))
for v, f, n, d in casse + regarder:
    print("   %-11s %s / %s — %s" % (v, f, n, d))
sys.exit(1 if casse else 0)
